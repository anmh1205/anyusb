package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Microsoft/go-winio"
)

func TestCLINamedPipe(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\anyusb-cli-test-%d`, os.Getpid())
	pipe, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/doctor" || r.Host != "anyusb" {
			t.Errorf("unexpected local request %s %s", r.Host, r.URL.Path)
		}
		fmt.Fprint(w, `[{"name":"USB/IP fixture","ok":true,"detail":"named pipe"}]`)
	})}
	go server.Serve(pipe)
	t.Cleanup(func() { _ = server.Close(); _ = pipe.Close() })
	var result bytes.Buffer
	if err := runCLI(t.Context(), []string{"-socket", path, "doctor"}, &result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.String(), "USB/IP fixture") || !strings.Contains(result.String(), "named pipe") {
		t.Fatal(result.String())
	}
}
