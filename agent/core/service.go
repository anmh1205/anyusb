// Package core exposes the local agent API independently of the operating system.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

const (
	DefaultSocket = "/run/anyusb/control.sock"
	LocalHost     = "anyusb"
	CallTimeout   = 35 * time.Second
)

type ConnectRequest struct {
	Host  string `json:"host"`
	BusID string `json:"bus_id"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func Handler(adapter platform.Adapter) http.Handler {
	router := http.NewServeMux()
	mutation := make(chan struct{}, 1)
	router.HandleFunc("GET /v1/devices", respond(func(r *http.Request) ([]platform.Device, error) {
		return adapter.ListLocalDevices(r.Context())
	}))
	router.Handle("PUT /v1/devices/{id}/export", serialize(mutation, respond(func(r *http.Request) (platform.Device, error) {
		return adapter.Export(r.Context(), r.PathValue("id"))
	})))
	router.Handle("DELETE /v1/devices/{id}/export", serialize(mutation, respond(func(r *http.Request) (platform.Device, error) {
		return adapter.Unexport(r.Context(), r.PathValue("id"))
	})))
	router.HandleFunc("GET /v1/remote-devices", respond(func(r *http.Request) ([]platform.RemoteDevice, error) {
		return adapter.ListRemote(r.Context(), r.URL.Query().Get("host"))
	}))
	router.HandleFunc("GET /v1/imports", respond(func(r *http.Request) ([]platform.Import, error) {
		return adapter.ListImports(r.Context())
	}))
	router.Handle("POST /v1/imports", serialize(mutation, respond(func(r *http.Request) (platform.Import, error) {
		var request ConnectRequest
		if err := decodeBody(r, &request); err != nil {
			return platform.Import{}, err
		}
		return adapter.Attach(r.Context(), request.Host, request.BusID)
	})))
	router.Handle("DELETE /v1/imports/{port}", serialize(mutation, respond(func(r *http.Request) (struct{}, error) {
		port, err := strconv.Atoi(r.PathValue("port"))
		if err != nil {
			return struct{}{}, fmt.Errorf("%w: specify a numeric virtual USB port", platform.ErrInvalid)
		}
		return struct{}{}, adapter.Detach(r.Context(), port)
	})))
	router.HandleFunc("GET /v1/doctor", respond(func(r *http.Request) ([]platform.Check, error) {
		return adapter.Doctor(r.Context()), nil
	}))
	router.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		streamEvents(w, r, adapter)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != LocalHost || r.Header.Get("Origin") != "" {
			writeJSON(w, http.StatusForbidden, ErrorResponse{Code: "forbidden", Message: "local Unix socket clients only"})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/events" {
			router.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), CallTimeout)
		defer cancel()
		router.ServeHTTP(w, r.WithContext(ctx))
	})
}

func serialize(lock chan struct{}, handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case lock <- struct{}{}:
			defer func() { <-lock }()
			if r.Context().Err() == nil {
				handler.ServeHTTP(w, r)
				return
			}
		case <-r.Context().Done():
		}
		writeError(w, r.Context().Err())
	})
}

func decodeBody(r *http.Request, target any) error {
	const maxBytes = 4096
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return fmt.Errorf("%w: could not read JSON request of at most %d bytes", platform.ErrInvalid, maxBytes)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%w: %v", platform.ErrInvalid, err)
	}
	return nil
}

func writeError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "backend_error"
	switch {
	case errors.Is(err, platform.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, platform.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, platform.ErrConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, platform.ErrUnavailable):
		status, code = http.StatusServiceUnavailable, "unavailable"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status, code = http.StatusGatewayTimeout, "timeout"
	}
	writeJSON(w, status, ErrorResponse{Code: code, Message: err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func respond[T any](operation func(*http.Request) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		response, err := operation(r)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	}
}

func streamEvents(w http.ResponseWriter, r *http.Request, adapter platform.Adapter) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events, err := adapter.WatchHotplug(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	controller := http.NewResponseController(w)
	if err := controller.Flush(); err != nil {
		return
	}
	encoder := json.NewEncoder(w)
	for event := range events {
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return
		}
		if encoder.Encode(event) != nil || controller.Flush() != nil {
			return
		}
		if err := controller.SetWriteDeadline(time.Time{}); err != nil {
			return
		}
	}
}
