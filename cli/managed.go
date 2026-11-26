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
	"github.com/anmh1205/anyusb/agent/platform"
	"github.com/coder/websocket"
)

const meshPath = "/v1/mesh"

const managedWaitUsage = "usage: anyusb wait --connection ID [--for ready|attached] [--timeout DURATION]"

type managedConnectOptions struct {
	peer, device string
	confirmRisk  bool
	wait         time.Duration
	waitFor      string
}

type managedWaitOptions struct {
	connection string
	timeout    time.Duration
	waitFor    string
}

func peerPath(peer string) string { return meshPath + "/peers/" + url.PathEscape(peer) }

func managedCLI(ctx context.Context, c client, command string, args []string, out io.Writer, jsonOutput bool) (bool, error) {
	method, path, message := http.MethodGet, "", ""
	var body any
	var connectWait time.Duration
	var connectWaitFor string
	switch command {
	case "desktop-url":
		if len(args) != 0 {
			return true, fmt.Errorf("usage: anyusb desktop-url")
		}
		url, err := desktopURL(ctx)
		if err == nil {
			_, err = fmt.Fprintln(out, url)
		}
		return true, err
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
		device, managed, ok := parseManagedTargetFlag(args, "--managed")
		if !managed {
			return false, nil
		}
		if !ok {
			return true, fmt.Errorf("usage: anyusb %s --managed DEVICE", command)
		}
		path = meshPath + "/shares/" + url.PathEscape(device)
		method, message = http.MethodPut, "Managed sharing enabled"
		if command == "unshare" {
			method, message = http.MethodDelete, "Managed sharing disabled"
		}
	case "force-release":
		device, hasFlag, ok := parseManagedTargetFlag(args, "--device")
		if !hasFlag || !ok {
			return true, fmt.Errorf("usage: anyusb force-release --device DEVICE")
		}
		method, path = http.MethodPost, meshPath+"/shares/"+url.PathEscape(device)+"/force-release"
	case "connect":
		if !hasManagedOption(args, "--peer") {
			return false, nil
		}
		options, err := parseManagedConnect(args)
		if err != nil {
			return true, err
		}
		method, path = http.MethodPost, meshPath+"/connections"
		body = map[string]any{"peer": options.peer, "device": options.device, "confirm_risk": options.confirmRisk}
		connectWait, connectWaitFor = options.wait, options.waitFor
	case "disconnect":
		if !hasManagedOption(args, "--connection") && !hasManagedOption(args, "--lease") {
			return false, nil
		}
		id, hasConn, ok := parseManagedTargetFlag(args, "--connection")
		if !hasConn {
			id, _, ok = parseManagedTargetFlag(args, "--lease")
		}
		if !ok {
			return true, fmt.Errorf("usage: anyusb disconnect --connection ID")
		}
		method, path, message = http.MethodDelete, meshPath+"/connections/"+url.PathEscape(id), "Connection disconnected"
	case "probe":
		if len(args) != 3 || args[0] != "--connection" {
			return true, fmt.Errorf("usage: anyusb probe --connection ID stlink|jlink|cmsis-dap")
		}
		method, path = http.MethodPost, meshPath+"/connections/"+url.PathEscape(args[1])+"/probe"
		body = map[string]string{"profile": args[2]}
	case "watch":
		if len(args) != 2 || args[0] != "--peer" {
			return false, nil
		}
		return true, readStream(ctx, c, peerPath(args[1])+"/events", func(bytes []byte) (bool, error) {
			_, err := fmt.Fprintln(out, string(bytes))
			return false, err
		})
	case "wait":
		options, err := parseManagedWait(args)
		if err != nil {
			return true, err
		}
		waitCtx, cancel := context.WithTimeout(ctx, options.timeout)
		defer cancel()
		connection, err := waitConnection(waitCtx, c, options.connection, options.waitFor, 250*time.Millisecond)
		if err != nil {
			return true, err
		}
		return true, writeManagedValue(out, connection, jsonOutput)
	case "chip-id", "erase", "reset", "flash", "verify", "monitor":
		return true, runESP(ctx, c, command, args, out)
	default:
		return false, nil
	}
	value, err := call[json.RawMessage](ctx, c, method, path, body)
	if err != nil {
		return true, err
	}
	if command == "connect" && connectWait > 0 {
		var connection mesh.Connection
		if err := json.Unmarshal(value, &connection); err != nil {
			return true, fmt.Errorf("decode created connection: %w", err)
		}
		waitCtx, cancel := context.WithTimeout(ctx, connectWait)
		defer cancel()
		connection, err = waitConnection(waitCtx, c, connection.ID, connectWaitFor, 250*time.Millisecond)
		if err != nil {
			return true, err
		}
		value, err = json.Marshal(connection)
		if err != nil {
			return true, err
		}
	}
	if message != "" && !jsonOutput {
		_, err = fmt.Fprintln(out, message)
		return true, err
	}
	if !jsonOutput {
		if handled, formatErr := formatManagedHuman(out, command, args, value); handled {
			return true, formatErr
		}
	}
	return true, writeManagedRaw(out, value, jsonOutput)
}

func hasManagedOption(args []string, option string) bool {
	for _, arg := range args {
		if arg == option {
			return true
		}
	}
	return false
}

func parseManagedTargetFlag(args []string, flagName string) (target string, present, ok bool) {
	for i := 0; i < len(args); i++ {
		if args[i] == flagName {
			present = true
			continue
		}
		if target != "" {
			return "", present, false
		}
		target = args[i]
	}
	return target, present, present && target != "" && len(args) == 2
}

func formatManagedHuman(out io.Writer, command string, args []string, raw json.RawMessage) (bool, error) {
	switch command {
	case "peers":
		var peers []mesh.Peer
		if err := json.Unmarshal(raw, &peers); err != nil {
			return false, err
		}
		return true, printTable(out, []string{"NAME", "PEER ID", "ENDPOINT"}, peers, func(p mesh.Peer) []string {
			return []string{p.Name, p.ID, p.Endpoint}
		})
	case "discover":
		var found []mesh.Discovered
		if err := json.Unmarshal(raw, &found); err != nil {
			return false, err
		}
		return true, printTable(out, []string{"NAME", "PEER ID", "ENDPOINT", "NETWORK"}, found, func(d mesh.Discovered) []string {
			return []string{d.Name, d.ID, d.Endpoint, d.Network}
		})
	case "list":
		if len(args) == 1 && args[0] == "--connections" {
			var links []mesh.Connection
			if err := json.Unmarshal(raw, &links); err != nil {
				return false, err
			}
			return true, printTable(out, []string{"CONNECTION", "PEER", "DEVICE", "STATE", "READINESS", "LOCAL BINDINGS"}, links, func(c mesh.Connection) []string {
				readiness, bindings := "", ""
				if c.Import != nil {
					readiness = c.Import.Readiness
					bindings = bindingList(c.Import.Bindings)
				}
				return []string{c.ID, c.Peer, c.Device, c.State, readiness, bindings}
			})
		}
	case "pair":
		if len(args) == 1 && args[0] == "invite" {
			var inv struct {
				Code    string    `json:"code"`
				Expires time.Time `json:"expires"`
			}
			if err := json.Unmarshal(raw, &inv); err != nil {
				return false, err
			}
			_, err := fmt.Fprintf(out, "Invite code: %s (expires %s)\n", inv.Code, inv.Expires.Local().Format("15:04:05"))
			return true, err
		}
		var peer mesh.Peer
		if err := json.Unmarshal(raw, &peer); err != nil {
			return false, err
		}
		_, err := fmt.Fprintf(out, "Paired with %s (%s) at %s\n", peer.Name, peer.ID, peer.Endpoint)
		return true, err
	case "peer-address":
		var peer mesh.Peer
		if err := json.Unmarshal(raw, &peer); err != nil {
			return false, err
		}
		_, err := fmt.Fprintf(out, "Updated %s (%s) endpoint to %s\n", peer.Name, peer.ID, peer.Endpoint)
		return true, err
	case "force-release":
		var fence mesh.LeaseFence
		if err := json.Unmarshal(raw, &fence); err != nil {
			return false, err
		}
		_, err := fmt.Fprintf(out, "Force-released %s from peer %s (cooldown until %s)\n", fence.Device, fence.Peer, fence.Until.Local().Format("15:04:05"))
		return true, err
	case "connect":
		var conn mesh.Connection
		if err := json.Unmarshal(raw, &conn); err != nil {
			return false, err
		}
		return true, printConnectionSummary(out, conn)
	case "probe":
		var res platform.ProbeResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return false, err
		}
		msg := fmt.Sprintf("Probe %s: ok=%t matched=%t", res.Profile, res.OK, res.Matched)
		if res.Serial != "" {
			msg += " serial=" + res.Serial
		}
		if res.Error != "" {
			msg += " error=" + res.Error
		}
		_, err := fmt.Fprintln(out, msg)
		return true, err
	}
	return false, nil
}

func printConnectionSummary(out io.Writer, conn mesh.Connection) error {
	line := fmt.Sprintf("Connection %s: state=%s", conn.ID, conn.State)
	if conn.Phase != "" {
		line += " phase=" + conn.Phase
	}
	if conn.Import != nil {
		if conn.Import.Readiness != "" {
			line += " readiness=" + conn.Import.Readiness
		}
		if len(conn.Import.SerialPorts) > 0 {
			line += " ports=" + strings.Join(conn.Import.SerialPorts, ",")
		} else if len(conn.Import.Bindings) > 0 {
			line += " bindings=" + bindingList(conn.Import.Bindings)
		}
	}
	if conn.Error != "" {
		line += " (" + conn.Error + ")"
	}
	_, err := fmt.Fprintln(out, line)
	return err
}

func parseManagedConnect(args []string) (managedConnectOptions, error) {
	const usage = "usage: anyusb connect --peer PEER DEVICE [--confirm-risk] [--wait DURATION] [--wait-for ready|attached]"
	options := managedConnectOptions{waitFor: "ready"}
	waitForSet := false
	for index := 0; index < len(args); index++ {
		option := args[index]
		switch option {
		case "--peer":
			if index+1 >= len(args) || options.peer != "" {
				return options, fmt.Errorf("%s", usage)
			}
			options.peer = args[index+1]
			index++
		case "--confirm-risk":
			if options.confirmRisk {
				return options, fmt.Errorf("%s", usage)
			}
			options.confirmRisk = true
		case "--wait", "--wait-for":
			if index+1 >= len(args) {
				return options, fmt.Errorf("%s", usage)
			}
			value := args[index+1]
			index++
			if option == "--wait" {
				if options.wait != 0 {
					return options, fmt.Errorf("%s", usage)
				}
				duration, err := time.ParseDuration(value)
				if err != nil || duration <= 0 {
					return options, fmt.Errorf("invalid wait duration %q", value)
				}
				options.wait = duration
			} else {
				if waitForSet || (value != "ready" && value != "attached") {
					return options, fmt.Errorf("%s", usage)
				}
				options.waitFor, waitForSet = value, true
			}
		default:
			if strings.HasPrefix(option, "-") || options.device != "" {
				return options, fmt.Errorf("%s", usage)
			}
			options.device = option
		}
	}
	if options.peer == "" || options.device == "" {
		return options, fmt.Errorf("%s", usage)
	}
	if waitForSet && options.wait == 0 {
		return options, fmt.Errorf("--wait-for requires --wait")
	}
	return options, nil
}

func parseManagedWait(args []string) (managedWaitOptions, error) {
	options := managedWaitOptions{timeout: 30 * time.Second, waitFor: "ready"}
	if len(args) < 2 || args[0] != "--connection" || args[1] == "" {
		return options, fmt.Errorf("%s", managedWaitUsage)
	}
	options.connection = args[1]
	for index := 2; index < len(args); index += 2 {
		if index+1 >= len(args) {
			return options, fmt.Errorf("%s", managedWaitUsage)
		}
		value := args[index+1]
		switch args[index] {
		case "--for":
			if value != "ready" && value != "attached" {
				return options, fmt.Errorf("%s", managedWaitUsage)
			}
			options.waitFor = value
		case "--timeout":
			duration, err := time.ParseDuration(value)
			if err != nil || duration <= 0 {
				return options, fmt.Errorf("invalid wait timeout %q", value)
			}
			options.timeout = duration
		default:
			return options, fmt.Errorf("%s", managedWaitUsage)
		}
	}
	return options, nil
}

func waitConnection(ctx context.Context, c client, id, target string, interval time.Duration) (mesh.Connection, error) {
	if target != "ready" && target != "attached" {
		return mesh.Connection{}, fmt.Errorf("invalid connection wait target %q", target)
	}
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	var last mesh.Connection
	for {
		connections, err := call[[]mesh.Connection](ctx, c, http.MethodGet, meshPath+"/connections", nil)
		if err != nil {
			if ctx.Err() != nil {
				return last, connectionWaitError(id, target, last, ctx.Err())
			}
			return last, err
		}
		found := false
		for _, connection := range connections {
			if connection.ID != id {
				continue
			}
			last, found = connection, true
			if connectionReached(connection, target) {
				return connection, nil
			}
			break
		}
		if !found {
			return last, fmt.Errorf("connection %q not found", id)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last, connectionWaitError(id, target, last, ctx.Err())
		case <-timer.C:
		}
	}
}

func connectionWaitError(id, target string, connection mesh.Connection, err error) error {
	return fmt.Errorf("wait for connection %s to become %s: %w (last state: %s)", id, target, err, connectionWaitState(connection))
}

func connectionReached(connection mesh.Connection, target string) bool {
	if connection.State != "attached" || connection.Import == nil {
		return false
	}
	return target == "attached" || connection.Import.Readiness == "ready"
}

func connectionWaitState(connection mesh.Connection) string {
	state := connection.State
	if state == "" {
		state = "unknown"
	}
	if connection.Import != nil {
		readiness := connection.Import.Readiness
		if readiness == "" {
			readiness = "unreported"
		}
		state += "/" + readiness
	}
	if connection.Phase != "" {
		state += " phase=" + connection.Phase
	}
	if connection.Attempt > 0 {
		state += " attempt=" + strconv.Itoa(connection.Attempt)
	}
	if connection.NextRetryAt != nil {
		state += " next=" + connection.NextRetryAt.Format(time.RFC3339)
	}
	if connection.LastFailure != nil {
		state += " code=" + connection.LastFailure.Code + " operation=" + connection.LastFailure.Operation
	}
	if connection.Error != "" {
		state += ": " + connection.Error
	}
	return state
}

func writeManagedValue(out io.Writer, value any, jsonOutput bool) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeManagedRaw(out, raw, jsonOutput)
}

func writeManagedRaw(out io.Writer, value json.RawMessage, jsonOutput bool) error {
	var formatted strings.Builder
	if jsonOutput {
		_, err := fmt.Fprintln(out, string(value))
		return err
	} else {
		encoder := json.NewEncoder(&formatted)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(value); err != nil {
			return err
		}
		_, err := fmt.Fprint(out, formatted.String())
		return err
	}
}

func showShared(out io.Writer, devices []mesh.SharedDevice, jsonOutput bool, err error) error {
	if err != nil {
		return err
	}
	if jsonOutput {
		return json.NewEncoder(out).Encode(devices)
	}
	return printTable(out, []string{"DEVICE ID", "BUSID", "PROFILE", "RISK", "STATE", "LEASE HOLDER", "DEVICE"}, devices, func(d mesh.SharedDevice) []string {
		state := "Absent"
		if d.Present {
			state = "Available"
		}
		if d.Mode != "" {
			state = d.Mode
		}
		return []string{d.ID, d.BusID, strings.Join(d.Profiles, ","), d.Risk, state, d.Owner, strings.TrimSpace(d.Manufacturer + " " + d.Product)}
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
