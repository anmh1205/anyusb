package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/anmh1205/anyusb/agent/esp"
	"github.com/anmh1205/anyusb/agent/platform"
	"github.com/coder/websocket"
)

type pairRequest struct{ Endpoint, Code string }

var requestIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{12,80}$`)

func (n *Node) ConfigureESP(python string) { n.esp = esp.New(n.ctx, n.dir, python, n.BeginESP) }

func (n *Node) ESPCheck() platform.Check {
	if n.esp == nil {
		return platform.Check{Name: "esptool", Detail: "ESP operations disabled"}
	}
	return n.esp.Doctor()
}

func (n *Node) LocalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/mesh/info", output(func(*http.Request) (Peer, error) {
		return Peer{ID: n.id, Name: n.name, Endpoint: n.address}, nil
	}))
	mux.HandleFunc("POST /v1/mesh/invite", output(func(*http.Request) (invite, error) { return n.newInvite() }))
	mux.HandleFunc("POST /v1/mesh/pair", output(func(r *http.Request) (Peer, error) {
		var input pairRequest
		if err := inputJSON(r, &input); err != nil {
			return Peer{}, err
		}
		return n.pair(r.Context(), input.Endpoint, input.Code)
	}))
	mux.HandleFunc("GET /v1/mesh/discovery", output(func(r *http.Request) ([]Discovered, error) { return Discover(r.Context()) }))
	mux.HandleFunc("GET /v1/mesh/peers", output(func(*http.Request) ([]Peer, error) { return n.peers(), nil }))
	mux.HandleFunc("PUT /v1/mesh/peers/{peer}", output(func(r *http.Request) (Peer, error) {
		var input struct{ Endpoint string }
		if err := inputJSON(r, &input); err != nil {
			return Peer{}, err
		}
		id := r.PathValue("peer")
		endpoint, err := PeerEndpoint(input.Endpoint)
		if err != nil {
			return Peer{}, err
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		peer, exists := n.config.Peers[id]
		if !exists {
			return Peer{}, platform.ErrNotFound
		}
		peer.Endpoint = endpoint
		return peer, n.addPeerLocked(peer)
	}))
	mux.HandleFunc("DELETE /v1/mesh/peers/{peer}", empty(func(r *http.Request) error { return n.revoke(r.PathValue("peer")) }))
	mux.HandleFunc("GET /v1/mesh/shares", output(func(r *http.Request) ([]SharedDevice, error) { return n.shared(r.Context()) }))
	mux.HandleFunc("PUT /v1/mesh/shares/{device}", empty(func(r *http.Request) error { return n.setShared(r.Context(), r.PathValue("device"), true) }))
	mux.HandleFunc("DELETE /v1/mesh/shares/{device}", empty(func(r *http.Request) error { return n.setShared(r.Context(), r.PathValue("device"), false) }))
	mux.HandleFunc("GET /v1/mesh/connections", output(func(*http.Request) ([]Connection, error) { return n.connectionViews(), nil }))
	mux.HandleFunc("POST /v1/mesh/connections", output(func(r *http.Request) (Connection, error) {
		var input struct{ Peer, Device string }
		if err := inputJSON(r, &input); err != nil {
			return Connection{}, err
		}
		peer, err := n.findPeer(input.Peer)
		if err != nil {
			return Connection{}, err
		}
		return n.connect(r.Context(), peer, input.Device)
	}))
	mux.HandleFunc("DELETE /v1/mesh/connections/{id}", empty(func(r *http.Request) error { return n.disconnect(r.Context(), r.PathValue("id")) }))
	mux.HandleFunc("GET /v1/mesh/peers/{peer}/devices", n.forward(false, func(*http.Request) string { return "/v1/shared" }))
	mux.HandleFunc("GET /v1/mesh/peers/{peer}/events", n.forward(true, func(*http.Request) string { return "/v1/events" }))
	mux.HandleFunc("POST /v1/mesh/peers/{peer}/esp/{device}/jobs", n.forward(false, func(r *http.Request) string { return "/v1/esp/" + url.PathEscape(r.PathValue("device")) + "/jobs" }))
	mux.HandleFunc("GET /v1/mesh/peers/{peer}/esp/{device}/monitor", n.forward(true, func(r *http.Request) string { return "/v1/esp/" + url.PathEscape(r.PathValue("device")) + "/monitor" }))
	mux.HandleFunc("GET /v1/mesh/peers/{peer}/jobs/{id}", n.forward(false, func(r *http.Request) string { return "/v1/jobs/" + url.PathEscape(r.PathValue("id")) }))
	mux.HandleFunc("GET /v1/mesh/peers/{peer}/jobs/{id}/logs", n.forward(true, func(r *http.Request) string { return "/v1/jobs/" + url.PathEscape(r.PathValue("id")) + "/logs" }))
	return mux
}

func (n *Node) PeerHandler() http.Handler {
	pair := http.NewServeMux()
	pair.HandleFunc("GET /v1/pair/info", empty(func(*http.Request) error { return nil }))
	pair.HandleFunc("POST /v1/pair/start", output(func(r *http.Request) (pairReply, error) {
		var input pairStart
		if err := inputJSON(r, &input); err != nil {
			return pairReply{}, err
		}
		if input.Endpoint == "" {
			if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				input.Endpoint = net.JoinHostPort(host, DefaultPeerPort)
			}
		}
		return n.beginPair(peerID(r), input)
	}))
	pair.HandleFunc("POST /v1/pair/finish", empty(func(r *http.Request) error {
		var input pairFinish
		if err := inputJSON(r, &input); err != nil {
			return err
		}
		return n.finishPair(peerID(r), input)
	}))
	peer := http.NewServeMux()
	peer.HandleFunc("GET /v1/shared", output(func(r *http.Request) ([]SharedDevice, error) { return n.shared(r.Context()) }))
	peer.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) { n.serveEvents(w, r) })
	peer.HandleFunc("POST /v1/leases", output(func(r *http.Request) (Lease, error) {
		var input struct{ Device string }
		if err := inputJSON(r, &input); err != nil {
			return Lease{}, err
		}
		return n.acquire(r.Context(), peerID(r), input.Device, "usbip")
	}))
	peer.HandleFunc("PUT /v1/leases/{id}", output(func(r *http.Request) (Lease, error) { return n.renew(peerID(r), r.PathValue("id")) }))
	peer.HandleFunc("DELETE /v1/leases/{id}", empty(func(r *http.Request) error { return n.release(peerID(r), r.PathValue("id")) }))
	peer.HandleFunc("GET /v1/tunnel/{id}", func(w http.ResponseWriter, r *http.Request) { n.serveTunnel(w, r, peerID(r), r.PathValue("id")) })
	peer.HandleFunc("POST /v1/esp/{device}/jobs", func(w http.ResponseWriter, r *http.Request) {
		if n.esp == nil {
			problem(w, platform.ErrUnavailable)
			return
		}
		id := r.Header.Get("Idempotency-Key")
		if id == "" {
			id = randomID()
		}
		if !requestIDPattern.MatchString(id) {
			problem(w, fmt.Errorf("%w: invalid Idempotency-Key", platform.ErrInvalid))
			return
		}
		job, err := n.esp.Start(w, r, peerID(r), r.PathValue("device"), id)
		if err != nil {
			problem(w, err)
			return
		}
		jsonResponse(w, http.StatusAccepted, job)
	})
	peer.HandleFunc("GET /v1/jobs/{id}", output(func(r *http.Request) (esp.Frame, error) {
		if n.esp == nil {
			return esp.Frame{}, platform.ErrUnavailable
		}
		job, err := n.esp.Job(peerID(r), r.PathValue("id"))
		if err != nil {
			return esp.Frame{}, err
		}
		frame, _ := job.Snapshot(0)
		return frame, nil
	}))
	peer.HandleFunc("GET /v1/jobs/{id}/logs", n.serveLogs)
	peer.HandleFunc("GET /v1/esp/{device}/monitor", func(w http.ResponseWriter, r *http.Request) {
		if n.esp == nil {
			problem(w, platform.ErrUnavailable)
			return
		}
		if err := n.esp.Monitor(w, r, peerID(r), r.PathValue("device")); err != nil {
			problem(w, err)
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.Header.Get("Origin") != "" {
			problem(w, failure(http.StatusForbidden, "peer endpoint requires an agent TLS connection"))
			return
		}
		switch r.URL.Path {
		case "/v1/pair/info", "/v1/pair/start", "/v1/pair/finish":
			pair.ServeHTTP(w, r)
		default:
			id := peerID(r)
			if !n.trusted(id) {
				problem(w, failure(http.StatusUnauthorized, "unpaired peer"))
				return
			}
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			go n.watchTrust(ctx, cancel, id)
			peer.ServeHTTP(w, r.WithContext(ctx))
		}
	})
}

func (n *Node) trusted(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.config.Peers[id]
	return ok
}

func (n *Node) watchTrust(ctx context.Context, cancel context.CancelFunc, id string) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !n.trusted(id) {
				cancel()
				return
			}
		}
	}
}

func (n *Node) revoke(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	peer, ok := n.config.Peers[id]
	if !ok {
		return nil
	}
	delete(n.config.Peers, id)
	if err := n.saveLocked(); err != nil {
		n.config.Peers[id] = peer
		return err
	}
	for _, le := range n.leases {
		if le.Owner == id && !le.busy {
			n.closeLeaseLocked(le)
		}
	}
	for _, conn := range n.connections {
		if conn.Peer == id {
			conn.cancel()
		}
	}
	return nil
}

func (n *Node) peers() []Peer {
	n.mu.Lock()
	defer n.mu.Unlock()
	peers := make([]Peer, 0, len(n.config.Peers))
	for _, peer := range n.config.Peers {
		peers = append(peers, peer)
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })
	return peers
}

func (n *Node) findPeer(id string) (Peer, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	peer, ok := n.config.Peers[id]
	if !ok {
		return Peer{}, fmt.Errorf("%w: peer %q", platform.ErrNotFound, id)
	}
	if peer.Endpoint == "" {
		return Peer{}, fmt.Errorf("%w: set the peer address first", platform.ErrInvalid)
	}
	return peer, nil
}

func (n *Node) connectionViews() []Connection {
	n.mu.Lock()
	defer n.mu.Unlock()
	views := make([]Connection, 0, len(n.connections))
	for _, conn := range n.connections {
		views = append(views, conn.Connection)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	return views
}

func (n *Node) forward(upgrade bool, path func(*http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peer, err := n.findPeer(r.PathValue("peer"))
		if err != nil {
			problem(w, err)
			return
		}
		query := r.URL.Query()
		query.Del("token")
		target := peerURL(peer.Endpoint, path(r)+"?"+query.Encode())
		client := n.peerClient(peer.ID)
		defer client.CloseIdleConnections()
		if upgrade {
			proxySocket(w, r, client, target)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		body := http.MaxBytesReader(w, r.Body, esp.MaxUpload)
		out, err := http.NewRequestWithContext(ctx, r.Method, target, body)
		if err != nil {
			problem(w, err)
			return
		}
		out.ContentLength = r.ContentLength
		out.Header.Set("Content-Type", r.Header.Get("Content-Type"))
		out.Header.Set("Idempotency-Key", r.Header.Get("Idempotency-Key"))
		res, err := client.Do(out)
		if err != nil {
			problem(w, failure(http.StatusBadGateway, err.Error()))
			return
		}
		defer res.Body.Close()
		w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}
}

func proxySocket(w http.ResponseWriter, r *http.Request, client *http.Client, target string) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	upstream, res, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		if res != nil {
			problem(w, failure(res.StatusCode, err.Error()))
		} else {
			problem(w, failure(http.StatusBadGateway, err.Error()))
		}
		return
	}
	defer upstream.CloseNow()
	downstream, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer downstream.CloseNow()
	upstream.SetReadLimit(2 << 20)
	downstream.SetReadLimit(32 << 10)
	done := make(chan struct{})
	go func() { defer close(done); defer cancel(); copyMessages(ctx, upstream, downstream) }()
	copyMessages(ctx, downstream, upstream)
	cancel()
	<-done
}

func copyMessages(ctx context.Context, dst, src *websocket.Conn) {
	for {
		kind, bytes, err := src.Read(ctx)
		if err != nil {
			return
		}
		if dst.Write(ctx, kind, bytes) != nil {
			return
		}
	}
}

func (n *Node) serveEvents(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx := ws.CloseRead(r.Context())
	events, unsubscribe := n.subscribe()
	defer unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok || writeSocket(ctx, ws, event) != nil {
				return
			}
		}
	}
}

func (n *Node) serveLogs(w http.ResponseWriter, r *http.Request) {
	if n.esp == nil {
		problem(w, platform.ErrUnavailable)
		return
	}
	job, err := n.esp.Job(peerID(r), r.PathValue("id"))
	if err != nil {
		problem(w, err)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx := ws.CloseRead(r.Context())
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	for {
		frame, update := job.Snapshot(after)
		if writeSocket(ctx, ws, frame) != nil {
			return
		}
		if len(frame.Logs) > 0 {
			after = frame.Logs[len(frame.Logs)-1].Number
		}
		if frame.Job.Status != "running" {
			_ = ws.Close(websocket.StatusNormalClosure, "job completed")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-update:
		}
	}
}

func writeSocket(ctx context.Context, ws *websocket.Conn, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return ws.Write(ctx, websocket.MessageText, payload)
}

func inputJSON(r *http.Request, target any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(data) > 8192 {
		return fmt.Errorf("%w: JSON body exceeds 8192 bytes", platform.ErrInvalid)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%w: %v", platform.ErrInvalid, err)
	}
	return nil
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func problem(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var response httpFailure
	switch {
	case errors.As(err, &response):
		status = response.Status
	case errors.Is(err, platform.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, platform.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, platform.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, platform.ErrUnavailable):
		status = http.StatusServiceUnavailable
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status = http.StatusGatewayTimeout
	}
	jsonResponse(w, status, map[string]string{"message": err.Error()})
}

func output[T any](operation func(*http.Request) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		value, err := operation(r)
		if err != nil {
			problem(w, err)
			return
		}
		jsonResponse(w, http.StatusOK, value)
	}
}

func empty(operation func(*http.Request) error) http.HandlerFunc {
	return output(func(r *http.Request) (struct{}, error) { return struct{}{}, operation(r) })
}
