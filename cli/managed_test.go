package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/anmh1205/anyusb/agent/mesh"
	"github.com/anmh1205/anyusb/agent/platform"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonClient(handler func(*http.Request) (int, any)) client {
	return client{socket: "test", http: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status, value := handler(request)
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(data)),
			Request:    request,
		}, nil
	})}}
}

func TestWaitConnectionRequiresRequestedReadiness(t *testing.T) {
	attempts := 0
	client := jsonClient(func(request *http.Request) (int, any) {
		if request.Method != http.MethodGet || request.URL.Path != meshPath+"/connections" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		attempts++
		readiness := "enumerating"
		if attempts >= 3 {
			readiness = "ready"
		}
		return http.StatusOK, []mesh.Connection{{
			ID: "connection", State: "attached", Import: &platform.Import{Readiness: readiness},
		}}
	})
	connection, err := waitConnection(t.Context(), client, "connection", "ready", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || connection.Import == nil || connection.Import.Readiness != "ready" {
		t.Fatalf("attempts = %d, connection = %+v", attempts, connection)
	}
}

func TestWaitConnectionTimeoutReportsLastEvidence(t *testing.T) {
	client := jsonClient(func(*http.Request) (int, any) {
		return http.StatusOK, []mesh.Connection{{ID: "connection", State: "reconnecting", Error: "peer unavailable"}}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
	defer cancel()
	_, err := waitConnection(ctx, client, "connection", "ready", 2*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "reconnecting: peer unavailable") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestConnectionWaitTargetsKeepTransportAndDriverEvidenceSeparate(t *testing.T) {
	connection := mesh.Connection{ID: "connection", State: "attached", Import: &platform.Import{Readiness: "enumerating"}}
	if !connectionReached(connection, "attached") {
		t.Fatal("attached target rejected a current native import")
	}
	if connectionReached(connection, "ready") {
		t.Fatal("ready target accepted an enumerating driver binding")
	}
	connection.Import.Readiness = "ready"
	if !connectionReached(connection, "ready") {
		t.Fatal("ready target rejected a ready driver binding")
	}
}

func TestManagedConnectCanWaitForReady(t *testing.T) {
	var requestBody map[string]any
	client := jsonClient(func(request *http.Request) (int, any) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == meshPath+"/connections":
			if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
				t.Fatal(err)
			}
			return http.StatusOK, mesh.Connection{ID: "connection", State: "connecting"}
		case request.Method == http.MethodGet && request.URL.Path == meshPath+"/connections":
			return http.StatusOK, []map[string]any{{
				"id": "connection", "state": "attached",
				"import": map[string]any{"port": 3, "readiness": "ready"},
			}}
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
			return http.StatusNotFound, map[string]string{"message": "unexpected request"}
		}
	})
	var output bytes.Buffer
	handled, err := managedCLI(t.Context(), client, "connect",
		[]string{"--peer", "peer", "device", "--confirm-risk", "--wait", "1s", "--wait-for", "ready"}, &output, true)
	if err != nil {
		t.Fatal(err)
	}
	if !handled || requestBody["peer"] != "peer" || requestBody["device"] != "device" || requestBody["confirm_risk"] != true {
		t.Fatalf("handled = %v, request = %#v", handled, requestBody)
	}
	var connection mesh.Connection
	if err := json.Unmarshal(output.Bytes(), &connection); err != nil {
		t.Fatalf("output %q: %v", output.String(), err)
	}
	if connection.ID != "connection" || connection.Import == nil || connection.Import.Readiness != "ready" {
		t.Fatalf("waited connection = %+v", connection)
	}
}

func TestManagedWaitArgumentValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--connection"},
		{"--connection", "id", "--for", "usable"},
		{"--connection", "id", "--timeout", "0s"},
	} {
		if _, err := parseManagedWait(args); err == nil {
			t.Fatalf("arguments %v were accepted", args)
		}
	}
	if _, err := parseManagedConnect([]string{"--peer", "peer", "device", "--wait-for", "ready"}); err == nil {
		t.Fatal("--wait-for without --wait was accepted")
	}
}

func TestForceReleaseUsesDeviceScopedEndpoint(t *testing.T) {
	client := jsonClient(func(request *http.Request) (int, any) {
		if request.Method != http.MethodPost || request.URL.EscapedPath() != meshPath+"/shares/device%2Fid/force-release" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.EscapedPath())
		}
		return http.StatusOK, mesh.LeaseFence{Peer: "peer", Device: "device/id", Generation: 2}
	})
	var output bytes.Buffer
	handled, err := managedCLI(t.Context(), client, "force-release", []string{"--device", "device/id"}, &output, true)
	if err != nil || !handled || !strings.Contains(output.String(), `"generation":2`) {
		t.Fatalf("force-release handled=%v output=%q err=%v", handled, output.String(), err)
	}
}

func TestManagedFlexibleFlagsAndHumanFormatting(t *testing.T) {
	client := jsonClient(func(request *http.Request) (int, any) {
		switch {
		case request.Method == http.MethodPut && request.URL.EscapedPath() == meshPath+"/shares/1-2":
			return http.StatusOK, map[string]any{}
		case request.Method == http.MethodPost && request.URL.Path == meshPath+"/invite":
			return http.StatusOK, map[string]any{"code": "1234", "expires": time.Date(2026, 11, 26, 14, 0, 0, 0, time.UTC)}
		case request.Method == http.MethodGet && request.URL.Path == meshPath+"/peers":
			return http.StatusOK, []mesh.Peer{{ID: "abcdef123456", Name: "Lab", Endpoint: "100.64.0.1:47443"}}
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
			return http.StatusNotFound, nil
		}
	})
	var shareOut bytes.Buffer
	if handled, err := managedCLI(t.Context(), client, "share", []string{"1-2", "--managed"}, &shareOut, false); err != nil || !handled {
		t.Fatalf("share with trailing --managed: handled=%v err=%v", handled, err)
	}
	var inviteOut bytes.Buffer
	if handled, err := managedCLI(t.Context(), client, "pair", []string{"invite"}, &inviteOut, false); err != nil || !handled || !strings.Contains(inviteOut.String(), "Invite code: 1234") {
		t.Fatalf("human invite output = %q (handled=%v err=%v)", inviteOut.String(), handled, err)
	}
	var peersOut bytes.Buffer
	if handled, err := managedCLI(t.Context(), client, "peers", nil, &peersOut, false); err != nil || !handled || !strings.Contains(peersOut.String(), "100.64.0.1:47443") || !strings.Contains(peersOut.String(), "PEER ID") {
		t.Fatalf("human peers table = %q (handled=%v err=%v)", peersOut.String(), handled, err)
	}
}
