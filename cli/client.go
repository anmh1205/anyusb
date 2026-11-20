package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/anmh1205/anyusb/agent/core"
)

type client struct {
	socket string
	http   *http.Client
}

func newClient(socket string) client {
	return client{socket: socket, http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				return dialLocal(ctx, socket)
			},
			ResponseHeaderTimeout: core.CallTimeout + time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c client) request(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}
	return c.requestBody(ctx, method, path, body, contentType)
}

func (c client) requestBody(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://"+core.LocalHost+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contact anyusbd on %s: %w", c.socket, err)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return res, nil
	}
	defer res.Body.Close()
	var problem core.ErrorResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&problem); err == nil && problem.Message != "" {
		return nil, fmt.Errorf("%s", problem.Message)
	}
	return nil, fmt.Errorf("anyusbd returned %s", res.Status)
}

func call[T any](ctx context.Context, c client, method, path string, input any) (T, error) {
	var output T
	ctx, cancel := context.WithTimeout(ctx, core.CallTimeout+2*time.Second)
	defer cancel()
	res, err := c.request(ctx, method, path, input)
	if err != nil {
		return output, err
	}
	defer res.Body.Close()
	err = json.NewDecoder(res.Body).Decode(&output)
	return output, err
}
