package mesh

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRetryPolicyUsesExponentialCapAndJitter(t *testing.T) {
	policy := RetryPolicy{Base: time.Second, Max: time.Minute, Jitter: func() float64 { return 0.5 }}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute}
	for index, expected := range want {
		if got := policy.Delay(index + 1); got != expected {
			t.Fatalf("attempt %d delay = %s, want %s", index+1, got, expected)
		}
	}
	policy.Jitter = func() float64 { return 0 }
	if got := policy.Delay(1); got != 800*time.Millisecond {
		t.Fatalf("minimum jitter delay = %s", got)
	}
	policy.Jitter = func() float64 { return 1 }
	if got := policy.Delay(1); got != 1200*time.Millisecond {
		t.Fatalf("maximum jitter delay = %s", got)
	}
	if got := policy.Delay(8); got != time.Minute {
		t.Fatalf("jitter exceeded the retry cap: %s", got)
	}
}

func TestConnectionFailureUsesTypedCodes(t *testing.T) {
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		err       error
		code      string
		retryable bool
	}{
		{"risk", withFailureCode("risk_required", errors.New("localized message may change")), "risk_required", false},
		{"revoked", httpFailure{Status: http.StatusUnauthorized, Message: "anything"}, "peer_revoked", false},
		{"temporary remote", httpFailure{Status: http.StatusServiceUnavailable, Message: "later"}, "lease_failed", true},
		{"timeout", context.DeadlineExceeded, "timeout", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := newConnectionFailure("lease", test.err, now)
			if failure.Code != test.code || failure.Retryable != test.retryable || failure.At != now {
				t.Fatalf("failure = %+v", failure)
			}
		})
	}
}

func TestRelayReportsDuplexProgress(t *testing.T) {
	leftBridge, leftClient := net.Pipe()
	rightBridge, rightClient := net.Pipe()
	defer leftClient.Close()
	defer rightClient.Close()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan RelayResult, 1)
	go func() { result <- relay(ctx, leftBridge, rightBridge) }()

	upload := []byte("local-to-peer")
	uploadWritten := make(chan error, 1)
	go func() {
		_, err := leftClient.Write(upload)
		uploadWritten <- err
	}()
	gotUpload := make([]byte, len(upload))
	if _, err := rightClient.Read(gotUpload); err != nil || string(gotUpload) != string(upload) {
		t.Fatalf("upload = %q, %v", gotUpload, err)
	}
	if err := <-uploadWritten; err != nil {
		t.Fatal(err)
	}

	download := []byte("peer-to-local")
	downloadWritten := make(chan error, 1)
	go func() {
		_, err := rightClient.Write(download)
		downloadWritten <- err
	}()
	gotDownload := make([]byte, len(download))
	if _, err := leftClient.Read(gotDownload); err != nil || string(gotDownload) != string(download) {
		t.Fatalf("download = %q, %v", gotDownload, err)
	}
	if err := <-downloadWritten; err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case got := <-result:
		if got.UploadBytes != uint64(len(upload)) || got.DownloadBytes != uint64(len(download)) ||
			got.LastProgress.IsZero() || got.Reason != "canceled" {
			t.Fatalf("relay result = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after cancellation")
	}
}

func TestRelayPublishesProgressBeforeDisconnect(t *testing.T) {
	leftBridge, leftClient := net.Pipe()
	rightBridge, rightClient := net.Pipe()
	defer leftClient.Close()
	defer rightClient.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	progress := make(chan relayPumpResult, 2)
	result := make(chan RelayResult, 1)
	go func() {
		result <- relayWithProgress(ctx, leftBridge, rightBridge, func(direction string, count uint64, _ time.Time) {
			progress <- relayPumpResult{direction: direction, bytes: count}
		})
	}()

	written := make(chan error, 1)
	go func() {
		_, err := leftClient.Write([]byte("live"))
		written <- err
	}()
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(rightClient, buffer); err != nil || string(buffer) != "live" {
		t.Fatalf("relay data = %q, %v", buffer, err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-progress:
		if update.direction != "upload" || update.bytes != 4 {
			t.Fatalf("live update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not publish progress while the connection was open")
	}
	select {
	case got := <-result:
		t.Fatalf("relay ended before cancellation: %+v", got)
	default:
	}
	cancel()
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after cancellation")
	}
}

func TestStableLeaseClearsRetryMetadata(t *testing.T) {
	node := &Node{}
	next := time.Now().Add(time.Minute)
	session := &connection{Connection: Connection{
		Attempt:     4,
		NextRetryAt: &next,
		LastFailure: &ConnectionFailure{Code: "lease_failed", Retryable: true},
	}}
	node.markConnectionStable(session)
	if session.Attempt != 0 || session.NextRetryAt != nil || session.LastFailure != nil {
		t.Fatalf("stable metadata = attempt %d, next %v, failure %+v", session.Attempt, session.NextRetryAt, session.LastFailure)
	}
}
