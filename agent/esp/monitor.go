package esp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type MonitorMessage struct {
	Data  []byte `json:"data,omitempty"`
	Baud  int    `json:"baud,omitempty"`
	DTR   *bool  `json:"dtr,omitempty"`
	RTS   *bool  `json:"rts,omitempty"`
	Error string `json:"error,omitempty"`
}

func (s *Service) Monitor(w http.ResponseWriter, r *http.Request, owner, device string) error {
	baud, err := baudRate(r.URL.Query().Get("baud"))
	if err != nil {
		return err
	}
	portName, release, err := s.begin(r.Context(), owner, device, r.URL.Query().Get("port"))
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

	port, err := s.opener(portName, baud)
	if err != nil {
		data, _ := json.Marshal(MonitorMessage{Error: err.Error()})
		_ = ws.Write(ctx, websocket.MessageText, data)
		return nil
	}
	var closeOnce sync.Once
	closePort := func() {
		closeOnce.Do(func() { _ = port.Close() })
	}
	defer closePort()
	_ = port.SetReadTimeout(50 * time.Millisecond)

	incoming := make(chan []byte, 32)
	readErr := make(chan error, 1)
	go monitorSerialReader(ctx, port, incoming, readErr)
	go monitorInput(ctx, ws, port, cancel)

	for {
		select {
		case <-ctx.Done():
			closePort()
			return nil
		case err := <-readErr:
			if err != nil && err != io.EOF {
				data, _ := json.Marshal(MonitorMessage{Error: err.Error()})
				_ = ws.Write(ctx, websocket.MessageText, data)
			}
			_ = ws.Close(websocket.StatusNormalClosure, "serial session ended")
			return nil
		case chunk := <-incoming:
			data, err := json.Marshal(MonitorMessage{Data: chunk})
			if err != nil || ws.Write(ctx, websocket.MessageText, data) != nil {
				return nil
			}
		}
	}
}

func monitorSerialReader(ctx context.Context, port SerialPort, out chan<- []byte, errOut chan<- error) {
	buf := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := port.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			select {
			case errOut <- err:
			case <-ctx.Done():
			}
			return
		}
	}
}

func monitorInput(ctx context.Context, ws *websocket.Conn, port SerialPort, cancel context.CancelFunc) {
	defer cancel()
	for {
		kind, payload, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var command MonitorMessage
		if kind != websocket.MessageText || json.Unmarshal(payload, &command) != nil || command.Error != "" || len(command.Data) > 16384 ||
			(command.Baud != 0 && (command.Baud < 300 || command.Baud > 4_000_000)) {
			_ = ws.Close(websocket.StatusPolicyViolation, "invalid serial command")
			return
		}
		if command.Baud != 0 {
			if err := port.SetBaud(command.Baud); err != nil {
				_ = ws.Close(websocket.StatusInternalError, fmt.Sprintf("set baud: %v", err))
				return
			}
		}
		if command.DTR != nil {
			if err := port.SetDTR(*command.DTR); err != nil {
				_ = ws.Close(websocket.StatusInternalError, fmt.Sprintf("set DTR: %v", err))
				return
			}
		}
		if command.RTS != nil {
			if err := port.SetRTS(*command.RTS); err != nil {
				_ = ws.Close(websocket.StatusInternalError, fmt.Sprintf("set RTS: %v", err))
				return
			}
		}
		if len(command.Data) > 0 {
			if _, err := port.Write(command.Data); err != nil {
				_ = ws.Close(websocket.StatusInternalError, fmt.Sprintf("write serial: %v", err))
				return
			}
		}
	}
}
