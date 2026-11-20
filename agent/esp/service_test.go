package esp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
	"github.com/coder/websocket"
)

func pythonFixture(t *testing.T) (string, string) {
	t.Helper()
	python, err := exec.LookPath("python")
	if err != nil {
		python, err = exec.LookPath("python3")
	}
	if err != nil {
		t.Skip("Python is unavailable for the host subprocess fixture")
	}
	module := t.TempDir()
	source := `import sys
if sys.argv[1:] == ['version']:
    print('5.2.0')
else:
    print('received ' + ' '.join(sys.argv[1:]), flush=True)
    print('verification log', file=sys.stderr, flush=True)
`
	if err := os.WriteFile(filepath.Join(module, "esptool.py"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PYTHONPATH", module)
	return python, module
}

func TestHostJobAndUploadReplay(t *testing.T) {
	python, _ := pythonFixture(t)
	var opens, releases atomic.Int32
	service := New(t.Context(), t.TempDir(), python, func(_ context.Context, owner, device, requested string) (string, func(), error) {
		if owner != "trusted-client" || device != "board" {
			return "", nil, errors.New("incorrect owner or device")
		}
		opens.Add(1)
		return "test-host-port", func() { releases.Add(1) }, nil
	})
	if diagnostic := service.Doctor(); !diagnostic.OK {
		t.Fatal(diagnostic.Detail)
	}
	start := func(content string) (JobState, error) {
		var dst bytes.Buffer
		multipart := multipart.NewWriter(&dst)
		if err := multipart.WriteField("manifest", `{"flash_files":{"0x10000":"app.bin"},"extra_esptool_args":{"chip":"esp32s3","after":"hard_reset"}}`); err != nil {
			t.Fatal(err)
		}
		part, err := multipart.CreateFormFile("files", "app.bin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := multipart.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "https://anyusb/esp?op=flash", &dst)
		req.Header.Set("Content-Type", multipart.FormDataContentType())
		return service.Start(httptest.NewRecorder(), req, "trusted-client", "board", "same-request-id")
	}
	job, err := start("real binary bytes")
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := start("real binary bytes"); err != nil || replay.ID != job.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := start("different binary"); !errors.Is(err, platform.ErrConflict) {
		t.Fatalf("payload conflict = %v", err)
	}
	active, err := service.Job("trusted-client", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Job("other-client", job.ID); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("log ownership = %v", err)
	}
	timeout := time.After(8 * time.Second)
	for {
		frame, updated := active.Snapshot(0)
		if frame.Job.Status == "succeeded" {
			var stdout, stderr string
			for _, item := range frame.Logs {
				if item.Stream == "stdout" {
					stdout += item.Text
				} else {
					stderr += item.Text
				}
			}
			if !strings.Contains(stdout, "--port test-host-port --baud 115200 --chip esp32s3 --after hard-reset write-flash 0x10000") ||
				!strings.Contains(stderr, "verification log") || opens.Load() != 1 || releases.Load() != 1 {
				t.Fatalf("job output or lease mismatch: %+v", frame)
			}
			return
		}
		if frame.Job.Status == "failed" {
			t.Fatalf("job failed: %+v", frame)
		}
		select {
		case <-updated:
		case <-timeout:
			t.Fatal("job did not finish")
		}
	}
}

func TestSerialMonitorProtocol(t *testing.T) {
	python, module := pythonFixture(t)
	fixture := `import queue
class Serial:
    def __init__(self, **kwargs):
        self.baudrate = kwargs['baudrate']
        self.data = queue.Queue()
    def open(self):
        self.data.put(b'BOOT')
    def close(self):
        pass
    @property
    def in_waiting(self):
        return 0
    def read(self, n):
        try:
            return self.data.get(timeout=0.05)
        except queue.Empty:
            return b''
    def write(self, data):
        self.data.put(str(self.baudrate).encode() + b':' + bytes([self.dtr, self.rts]) + data)
`
	if err := os.WriteFile(filepath.Join(module, "serial.py"), []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	service := New(t.Context(), t.TempDir(), python, func(context.Context, string, string, string) (string, func(), error) {
		return "test-host-port", func() { close(released) }, nil
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := service.Monitor(w, r, "trusted", "board"); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, server.URL+"?baud=115200", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	read := func() MonitorMessage {
		_, raw, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var message MonitorMessage
		if err := json.Unmarshal(raw, &message); err != nil || message.Error != "" {
			t.Fatalf("serial frame %s: %v", raw, err)
		}
		return message
	}
	if msg := read(); string(msg.Data) != "BOOT" {
		t.Fatalf("monitor output = %q", msg.Data)
	}
	on, off := true, false
	payload, _ := json.Marshal(MonitorMessage{Baud: 921600, DTR: &on, RTS: &off, Data: []byte("input")})
	if err := ws.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
	if msg := read(); string(msg.Data) != "921600:\x01\x00input" {
		t.Fatalf("monitor settings or input = %q", msg.Data)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "done")
	select {
	case <-released:
	case <-ctx.Done():
		t.Fatal("monitor did not release its lease")
	}
}
