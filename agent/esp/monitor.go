package esp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"

	"github.com/anmh1205/anyusb/agent/platform"
	"github.com/coder/websocket"
)

type MonitorMessage struct {
	Data  []byte `json:"data,omitempty"`
	Baud  int    `json:"baud,omitempty"`
	DTR   *bool  `json:"dtr,omitempty"`
	RTS   *bool  `json:"rts,omitempty"`
	Error string `json:"error,omitempty"`
}

const serialScript = `
import base64, json, serial, sys, threading

closed = threading.Event()
port = None
def reader():
    try:
        for line in sys.stdin:
            cmd = json.loads(line)
            if 'baud' in cmd: port.baudrate = cmd['baud']
            if 'dtr' in cmd: port.dtr = cmd['dtr']
            if 'rts' in cmd: port.rts = cmd['rts']
            if 'data' in cmd: port.write(base64.b64decode(cmd['data'], validate=True))
    finally:
        closed.set()

try:
    port = serial.Serial(port=None, baudrate=int(sys.argv[2]), timeout=0.1, write_timeout=2)
    port.dtr = False
    port.rts = False
    port.port = sys.argv[1]
    port.open()
    threading.Thread(target=reader, daemon=True).start()
    while not closed.is_set():
        data = port.read(min(port.in_waiting or 1, 4096))
        if data:
            print(json.dumps({'data': base64.b64encode(data).decode('ascii')}), flush=True)
except Exception as exc:
    print(json.dumps({'error': str(exc)}), flush=True)
finally:
    if port: port.close()
`

func (s *Service) Monitor(w http.ResponseWriter, r *http.Request, owner, device string) error {
	if s.versionErr != nil {
		return fmt.Errorf("%w: %v", platform.ErrUnavailable, s.versionErr)
	}
	baud, err := baudRate(r.URL.Query().Get("baud"))
	if err != nil {
		return err
	}
	port, release, err := s.begin(r.Context(), owner, device, r.URL.Query().Get("port"))
	if err != nil {
		return err
	}
	defer release()
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil
	}
	defer ws.CloseNow()
	ws.SetReadLimit(32 << 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, s.python, "-u", "-c", serialScript, port, strconv.Itoa(baud))
	cmd.Env = pythonEnv()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		data, _ := json.Marshal(MonitorMessage{Error: err.Error()})
		ws.Write(ctx, websocket.MessageText, data)
		return nil
	}
	defer cmd.Wait()
	defer cancel()
	go monitorInput(ctx, ws, stdin, cancel)
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if ws.Write(ctx, websocket.MessageText, scanner.Bytes()) != nil {
			break
		}
	}
	_ = ws.Close(websocket.StatusNormalClosure, "serial session ended")
	return nil
}

func monitorInput(ctx context.Context, ws *websocket.Conn, input io.WriteCloser, cancel context.CancelFunc) {
	defer cancel()
	defer input.Close()
	encoder := json.NewEncoder(input)
	for {
		kind, bytes, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var command MonitorMessage
		if kind != websocket.MessageText || json.Unmarshal(bytes, &command) != nil || command.Error != "" || len(command.Data) > 16384 ||
			(command.Baud != 0 && (command.Baud < 300 || command.Baud > 4_000_000)) {
			_ = ws.Close(websocket.StatusPolicyViolation, "invalid serial command")
			return
		}
		if encoder.Encode(command) != nil {
			return
		}
	}
}
