package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anmh1205/anyusb/agent/core"
	"github.com/anmh1205/anyusb/agent/esp"
	"github.com/anmh1205/anyusb/agent/mesh"
	"github.com/coder/websocket"
)

const meshPath = "/v1/mesh"

func peerPath(peer string) string { return meshPath + "/peers/" + url.PathEscape(peer) }

func managedCLI(ctx context.Context, c client, command string, args []string, out io.Writer, jsonOutput bool) (bool, error) {
	method, path, message := http.MethodGet, "", ""
	var body any
	switch command {
	case "ui":
		if len(args) != 0 {
			return true, fmt.Errorf("usage: anyusb ui")
		}
		response, err := call[map[string]string](ctx, c, method, "/v1/ui", nil)
		if err != nil {
			return true, err
		}
		_, err = fmt.Fprintln(out, response["url"])
		return true, err
	case "peers", "discover":
		if len(args) != 0 {
			return true, fmt.Errorf("usage: anyusb %s", command)
		}
		path = meshPath + "/peers"
		if command == "discover" {
			path = meshPath + "/discovery"
		}
	case "pair":
		method = http.MethodPost
		switch {
		case len(args) == 1 && args[0] == "invite":
			path = meshPath + "/invite"
		case len(args) == 2:
			path = meshPath + "/pair"
			body = map[string]string{"endpoint": args[0], "code": args[1]}
		default:
			return true, fmt.Errorf("usage: anyusb pair invite | pair HOST CODE")
		}
	case "revoke", "peer-address":
		switch {
		case command == "revoke" && len(args) == 1:
			method, path, message = http.MethodDelete, peerPath(args[0]), "Peer revoked"
		case command == "peer-address" && len(args) == 2:
			method, path = http.MethodPut, peerPath(args[0])
			body = map[string]string{"endpoint": args[1]}
		default:
			return true, fmt.Errorf("usage: anyusb revoke PEER | peer-address PEER HOST")
		}
	case "list":
		switch {
		case len(args) == 2 && args[0] == "--peer":
			d, err := call[[]mesh.SharedDevice](ctx, c, method, peerPath(args[1])+"/devices", nil)
			return true, showShared(out, d, jsonOutput, err)
		case len(args) == 1 && args[0] == "--shared":
			d, err := call[[]mesh.SharedDevice](ctx, c, method, meshPath+"/shares", nil)
			return true, showShared(out, d, jsonOutput, err)
		case len(args) == 1 && args[0] == "--connections":
			path = meshPath + "/connections"
		default:
			return false, nil
		}
	case "share", "unshare":
		if len(args) == 0 || args[0] != "--managed" {
			return false, nil
		}
		if len(args) != 2 {
			return true, fmt.Errorf("usage: anyusb %s --managed DEVICE", command)
		}
		path = meshPath + "/shares/" + url.PathEscape(args[1])
		method, message = http.MethodPut, "Managed sharing enabled"
		if command == "unshare" {
			method, message = http.MethodDelete, "Managed sharing disabled"
		}
	case "connect":
		if len(args) == 0 || args[0] != "--peer" {
			return false, nil
		}
		if len(args) != 3 {
			return true, fmt.Errorf("usage: anyusb connect --peer PEER DEVICE")
		}
		method, path = http.MethodPost, meshPath+"/connections"
		body = map[string]string{"peer": args[1], "device": args[2]}
	case "disconnect":
		if len(args) == 0 || args[0] != "--lease" {
			return false, nil
		}
		if len(args) != 2 {
			return true, fmt.Errorf("usage: anyusb disconnect --lease ID")
		}
		method, path, message = http.MethodDelete, meshPath+"/connections/"+url.PathEscape(args[1]), "Lease disconnected"
	case "watch":
		if len(args) != 2 || args[0] != "--peer" {
			return false, nil
		}
		return true, readStream(ctx, c, peerPath(args[1])+"/events", func(bytes []byte) (bool, error) {
			_, err := fmt.Fprintln(out, string(bytes))
			return false, err
		})
	case "chip-id", "erase", "reset", "flash", "verify", "monitor":
		return true, runESP(ctx, c, command, args, out)
	default:
		return false, nil
	}
	value, err := call[json.RawMessage](ctx, c, method, path, body)
	if err != nil {
		return true, err
	}
	if message != "" && !jsonOutput {
		_, err = fmt.Fprintln(out, message)
		return true, err
	}
	var formatted strings.Builder
	if jsonOutput {
		_, err = fmt.Fprintln(out, string(value))
	} else {
		encoder := json.NewEncoder(&formatted)
		encoder.SetIndent("", "  ")
		if err = encoder.Encode(value); err == nil {
			_, err = fmt.Fprint(out, formatted.String())
		}
	}
	return true, err
}

func showShared(out io.Writer, devices []mesh.SharedDevice, jsonOutput bool, err error) error {
	if err != nil {
		return err
	}
	if jsonOutput {
		return json.NewEncoder(out).Encode(devices)
	}
	return printTable(out, []string{"DEVICE ID", "BUSID", "STATE", "LEASE HOLDER", "DEVICE"}, devices, func(d mesh.SharedDevice) []string {
		state := "Absent"
		if d.Present {
			state = "Available"
		}
		if d.Mode != "" {
			state = d.Mode
		}
		return []string{d.ID, d.BusID, state, d.Owner, strings.TrimSpace(d.Manufacturer + " " + d.Product)}
	})
}

func runESP(ctx context.Context, c client, op string, args []string, out io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: anyusb %s PEER DEVICE [OPTIONS]", op)
	}
	peer, device := args[0], args[1]
	opts := parseESPFlags(op)
	if err := opts.flags.Parse(args[2:]); err != nil {
		return err
	}
	path := peerPath(peer) + "/esp/" + url.PathEscape(device)
	query := url.Values{"baud": {strconv.Itoa(*opts.baud)}, "port": {*opts.port}}
	if op == "monitor" {
		if opts.flags.NArg() != 0 || *opts.idf != "" {
			return fmt.Errorf("usage: anyusb monitor PEER DEVICE [--port PORT] [--baud BAUD] [--dtr] [--rts]")
		}
		return monitorCLI(ctx, c, path+"/monitor?"+query.Encode(), out, *opts.dtr, *opts.rts)
	}
	query.Set("op", op)
	path += "/jobs?" + query.Encode()
	var job esp.JobState
	var err error
	if op == "flash" || op == "verify" {
		job, err = submitFirmware(ctx, c, path, *opts.idf, opts.flags.Args())
	} else {
		if opts.flags.NArg() != 0 || *opts.idf != "" {
			return fmt.Errorf("unexpected ESP arguments")
		}
		job, err = call[esp.JobState](ctx, c, http.MethodPost, path, nil)
	}
	if err != nil {
		return err
	}
	final := false
	err = readStream(ctx, c, peerPath(peer)+"/jobs/"+url.PathEscape(job.ID)+"/logs", func(data []byte) (bool, error) {
		var frame esp.Frame
		if err := json.Unmarshal(data, &frame); err != nil {
			return true, err
		}
		for _, line := range frame.Logs {
			if _, err := fmt.Fprint(out, line.Text); err != nil {
				return true, err
			}
		}
		if frame.Job.Status == "failed" {
			final = true
			return true, fmt.Errorf("ESP job %s failed: %s", frame.Job.ID, frame.Job.Error)
		}
		final = frame.Job.Status == "succeeded"
		return final, nil
	})
	if !final {
		return fmt.Errorf("ESP job %s log stream ended before a final result: %v", job.ID, err)
	}
	return err
}

func submitFirmware(ctx context.Context, c client, endpoint, idf string, offsets []string) (esp.JobState, error) {
	var manifest esp.Manifest
	paths := make(map[string]string)
	if idf != "" {
		if len(offsets) > 0 {
			return esp.JobState{}, fmt.Errorf("choose --idf or explicit offsets")
		}
		var err error
		manifest, paths, err = esp.IDFManifest(idf)
		if err != nil {
			return esp.JobState{}, err
		}
	} else {
		manifest.FlashFiles = make(map[string]string)
		for i, arg := range offsets {
			offset, file, found := strings.Cut(arg, "=")
			if !found || offset == "" || file == "" || manifest.FlashFiles[offset] != "" {
				return esp.JobState{}, fmt.Errorf("invalid or duplicate OFFSET=FILE %q", arg)
			}
			name := fmt.Sprintf("%d-%s", i, filepath.Base(file))
			manifest.FlashFiles[offset], paths[name] = name, file
		}
	}
	if len(paths) == 0 {
		return esp.JobState{}, fmt.Errorf("supply --idf FILE or OFFSET=FILE entries")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	upload := multipart.NewWriter(writer)
	go func() { writer.CloseWithError(esp.WriteUpload(upload, manifest, paths)) }()
	res, err := c.requestBody(ctx, http.MethodPost, endpoint, reader, upload.FormDataContentType())
	if err != nil {
		return esp.JobState{}, err
	}
	defer res.Body.Close()
	var job esp.JobState
	err = json.NewDecoder(res.Body).Decode(&job)
	return job, err
}

func openSocket(ctx context.Context, c client, path string) (*websocket.Conn, error) {
	ws, res, err := websocket.Dial(ctx, "ws://"+core.LocalHost+path, &websocket.DialOptions{HTTPClient: c.http})
	if err != nil {
		if res != nil {
			return nil, fmt.Errorf("agent stream returned %s: %w", res.Status, err)
		}
		return nil, err
	}
	ws.SetReadLimit(4 << 20)
	return ws, nil
}

func readStream(ctx context.Context, c client, path string, receive func([]byte) (bool, error)) error {
	ws, err := openSocket(ctx, c, path)
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil
			}
			return err
		}
		done, err := receive(data)
		if done || err != nil {
			return err
		}
	}
}

func monitorCLI(ctx context.Context, c client, path string, out io.Writer, dtr, rts bool) error {
	ws, err := openSocket(ctx, c, path)
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	settings, _ := json.Marshal(esp.MonitorMessage{DTR: &dtr, RTS: &rts})
	if err := ws.Write(ctx, websocket.MessageText, settings); err != nil {
		return err
	}
	go func() {
		input := bufio.NewReader(os.Stdin)
		buf := make([]byte, 4096)
		for {
			n, err := input.Read(buf)
			if n > 0 {
				data, _ := json.Marshal(esp.MonitorMessage{Data: buf[:n]})
				if ws.Write(ctx, websocket.MessageText, data) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil
			}
			return err
		}
		var message esp.MonitorMessage
		if err := json.Unmarshal(data, &message); err != nil {
			return err
		}
		if message.Error != "" {
			return fmt.Errorf("serial monitor: %s", message.Error)
		}
		if _, err := out.Write(message.Data); err != nil {
			return err
		}
	}
}
