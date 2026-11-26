package mesh

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

type ConnectionFailure struct {
	Code      string    `json:"code"`
	Operation string    `json:"operation"`
	Retryable bool      `json:"retryable"`
	Message   string    `json:"message,omitempty"`
	At        time.Time `json:"at"`
}

type RetryPolicy struct {
	Base   time.Duration
	Max    time.Duration
	Jitter func() float64
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{Base: time.Second, Max: time.Minute, Jitter: rand.Float64}
}

func (policy RetryPolicy) Delay(attempt int) time.Duration {
	if policy.Base <= 0 {
		policy.Base = time.Second
	}
	if policy.Max <= 0 {
		policy.Max = time.Minute
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := policy.Base
	for current := 1; current < attempt && delay < policy.Max; current++ {
		if delay > policy.Max/2 {
			delay = policy.Max
			break
		}
		delay *= 2
	}
	delay = min(delay, policy.Max)
	random := 0.5
	if policy.Jitter != nil {
		random = min(max(policy.Jitter(), 0), 1)
	}
	// Scale to the closed interval [0.8, 1.2].
	return min(policy.Max, time.Duration(math.Round(float64(delay)*(0.8+0.4*random))))
}

type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type codedError struct {
	code string
	err  error
}

func (err codedError) Error() string       { return err.err.Error() }
func (err codedError) Unwrap() error       { return err.err }
func (err codedError) FailureCode() string { return err.code }

func withFailureCode(code string, err error) error {
	return codedError{code: code, err: err}
}

func failureCode(err error) string {
	var coded interface{ FailureCode() string }
	if errors.As(err, &coded) {
		return coded.FailureCode()
	}
	return ""
}

func newConnectionFailure(operation string, err error, now time.Time) *ConnectionFailure {
	if err == nil {
		return nil
	}
	code, retryable := failureCode(err), true
	var remote httpFailure
	switch {
	case code != "":
	case errors.Is(err, context.Canceled):
		code, retryable = "canceled", false
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.As(err, &remote) && (remote.Status == http.StatusUnauthorized || remote.Status == http.StatusForbidden):
		code, retryable = "peer_revoked", false
	case errors.As(err, &remote) && remote.Status == http.StatusNotFound:
		code = "device_absent"
	case errors.Is(err, platform.ErrInvalid):
		code, retryable = "invalid_device", false
	case errors.Is(err, platform.ErrNotFound) && operation == "peer_lookup":
		code, retryable = "peer_revoked", false
	case errors.Is(err, platform.ErrNotFound):
		code = "device_absent"
	case errors.Is(err, platform.ErrConflict):
		code = "device_busy"
	case errors.Is(err, platform.ErrUnavailable):
		code = "backend_unavailable"
	default:
		var networkError net.Error
		if errors.As(err, &networkError) {
			code = "dial_failed"
		} else {
			code = operation + "_failed"
		}
	}
	switch code {
	case "risk_required", "policy_blocked", "peer_revoked", "invalid_device", "canceled":
		retryable = false
	}
	return &ConnectionFailure{Code: code, Operation: operation, Retryable: retryable, Message: err.Error(), At: now}
}
