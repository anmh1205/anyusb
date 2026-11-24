package mesh

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
	"github.com/coder/websocket"
)

const tunnelProtocol = "anyusb-usbip-v1"
const leaseRenewGrace = 6 * time.Second

func (n *Node) serveTunnel(w http.ResponseWriter, r *http.Request, owner, token string) {
	n.mu.Lock()
	le := n.currentLeaseLocked(owner, token)
	if le == nil || le.Mode != "usbip" || le.Closing || !le.Online || time.Now().After(le.Expires) || le.stopTunnel != nil {
		n.mu.Unlock()
		problem(w, failure(http.StatusConflict, "USB/IP lease is unavailable"))
		return
	}
	busID := le.BusID
	ctx, stop := context.WithCancel(r.Context())
	le.stopTunnel = stop
	n.mu.Unlock()
	defer func() {
		stop()
		n.mu.Lock()
		le.stopTunnel = nil
		n.mu.Unlock()
	}()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{tunnelProtocol}})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	if ws.Subprotocol() != tunnelProtocol {
		return
	}
	stream := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	var request [40]byte
	if _, err := io.ReadFull(stream, request[:]); err != nil || !validImport(request, busID) {
		return
	}
	_ = stream.SetReadDeadline(time.Time{})
	tcp, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", n.backend)
	if err != nil {
		return
	}
	defer tcp.Close()
	stopRead := context.AfterFunc(ctx, func() { _ = tcp.Close() })
	defer stopRead()
	_ = tcp.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := tcp.Write(request[:]); err != nil {
		return
	}
	_ = tcp.SetWriteDeadline(time.Time{})
	_ = tcp.SetReadDeadline(time.Now().Add(12 * time.Second))
	var response [8]byte
	if _, err := io.ReadFull(tcp, response[:]); err != nil {
		return
	}
	_ = tcp.SetReadDeadline(time.Time{})
	_ = stream.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := stream.Write(response[:]); err != nil {
		return
	}
	_ = stream.SetWriteDeadline(time.Time{})
	if binary.BigEndian.Uint16(response[:2]) != 0x0111 || binary.BigEndian.Uint16(response[2:4]) != 0x0003 ||
		binary.BigEndian.Uint32(response[4:]) != 0 {
		return
	}
	n.mu.Lock()
	event := "attached"
	if le.attached {
		event = "reattached"
	}
	le.attached = true
	n.publishLocked(event, le.Device)
	n.mu.Unlock()
	relay(ctx, tcp, stream)
	n.mu.Lock()
	if !le.Closing && le.Online {
		n.publishLocked("lost", le.Device)
	}
	n.mu.Unlock()
}

func validImport(request [40]byte, busID string) bool {
	return binary.BigEndian.Uint16(request[0:2]) == 0x0111 &&
		binary.BigEndian.Uint16(request[2:4]) == 0x8003 &&
		binary.BigEndian.Uint32(request[4:8]) == 0 &&
		string(bytes.TrimRight(request[8:], "\x00")) == busID
}

func relay(ctx context.Context, left, right net.Conn) {
	closeBoth := func() {
		left.SetDeadline(time.Now())
		right.SetDeadline(time.Now())
	}
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(left, right)
		closeBoth()
		close(done)
	}()
	_, _ = io.Copy(right, left)
	closeBoth()
	<-done
}

type Connection struct {
	ID           string           `json:"id"`
	Peer         string           `json:"peer"`
	Device       string           `json:"device"`
	State        string           `json:"state"`
	Error        string           `json:"error,omitempty"`
	RiskAccepted bool             `json:"risk_accepted,omitempty"`
	DeviceInfo   *platform.Device `json:"device_info,omitempty"`
	Import       *platform.Import `json:"import,omitempty"`
}

type connection struct {
	Connection
	cancel context.CancelFunc
	done   chan struct{}
}

func (n *Node) connect(ctx context.Context, peer Peer, device string) (Connection, error) {
	return n.connectWithPolicy(ctx, peer, device, false)
}

func (n *Node) connectWithPolicy(ctx context.Context, peer Peer, device string, riskAccepted bool) (Connection, error) {
	n.clientMu.Lock()
	defer n.clientMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Connection{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, trusted := n.config.Peers[peer.ID]; !trusted {
		return Connection{}, failure(http.StatusForbidden, "peer has been revoked")
	}
	for _, intent := range n.config.Connections {
		if intent.Peer == peer.ID && intent.Device == device {
			if riskAccepted && !intent.RiskAccepted {
				intent.RiskAccepted = true
				n.config.Connections[intent.ID] = intent
				if err := n.saveLocked(); err != nil {
					return Connection{}, err
				}
			}
			if session := n.connections[intent.ID]; session != nil {
				session.RiskAccepted = intent.RiskAccepted
				return session.Connection, nil
			}
			return n.startConnectionLocked(intent), nil
		}
	}
	intent := connectionIntent{ID: randomID(), Peer: peer.ID, Device: device, RiskAccepted: riskAccepted}
	n.config.Connections[intent.ID] = intent
	if err := n.saveLocked(); err != nil {
		delete(n.config.Connections, intent.ID)
		return Connection{}, err
	}
	return n.startConnectionLocked(intent), nil
}

func (n *Node) restoreConnections() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, intent := range n.config.Connections {
		if intent.ID == "" || intent.Peer == "" || intent.Device == "" || n.config.Peers[intent.Peer].ID == "" {
			continue
		}
		n.startConnectionLocked(intent)
	}
}

func (n *Node) startConnectionLocked(intent connectionIntent) Connection {
	if current := n.connections[intent.ID]; current != nil {
		return current.Connection
	}
	lifetime, cancel := context.WithCancel(n.ctx)
	view := Connection{ID: intent.ID, Peer: intent.Peer, Device: intent.Device, State: "connecting", RiskAccepted: intent.RiskAccepted}
	session := &connection{Connection: view, cancel: cancel, done: make(chan struct{})}
	n.connections[intent.ID] = session
	go n.maintainConnection(lifetime, session)
	return view
}

func (n *Node) maintainConnection(ctx context.Context, session *connection) {
	defer close(session.done)
	defer func() {
		n.mu.Lock()
		if n.connections[session.ID] == session {
			delete(n.connections, session.ID)
		}
		n.publishLocked("disconnected", session.Device)
		n.mu.Unlock()
	}()
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		peer, err := n.findPeer(session.Peer)
		if err == nil {
			client := n.peerClient(peer.ID)
			n.mu.Lock()
			riskAccepted := session.RiskAccepted
			n.mu.Unlock()
			attemptCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			le, acquireErr := call[Lease](attemptCtx, client, http.MethodPost, peer.Endpoint, "/v1/leases", struct {
				Device      string `json:"device"`
				ConfirmRisk bool   `json:"confirm_risk"`
			}{session.Device, riskAccepted})
			cancel()
			if acquireErr == nil {
				listener, listenErr := net.Listen("tcp4", "127.0.0.1:0")
				if listenErr == nil {
					err = n.followLease(ctx, session, client, peer, le, listener)
				} else {
					n.releasePeer(client, peer, le.ID)
					err = listenErr
				}
			} else {
				err = acquireErr
			}
			client.CloseIdleConnections()
		}
		if ctx.Err() != nil {
			return
		}
		n.setConnection(session, "reconnecting", nil, err)
		attempt++
		delay := time.Duration(min(attempt, 5)) * time.Second
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (n *Node) acceptLocal(ctx context.Context, listener net.Listener, peer Peer, leaseID string, client *http.Client) {
	// The virtual controller is the only expected local client. The host accepts one active tunnel per lease.
	for {
		tcp, err := listener.Accept()
		if err != nil {
			return
		}
		func() {
			defer tcp.Close()
			target := peerURL(peer.Endpoint, "/v1/tunnel/"+url.PathEscape(leaseID))
			ws, _, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPClient: client, Subprotocols: []string{tunnelProtocol}})
			if err != nil {
				return
			}
			defer ws.CloseNow()
			if ws.Subprotocol() == tunnelProtocol {
				relay(ctx, tcp, websocket.NetConn(ctx, ws, websocket.MessageBinary))
			}
		}()
	}
}

func (n *Node) releasePeer(client *http.Client, peer Peer, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = call[struct{}](ctx, client, http.MethodDelete, peer.Endpoint, "/v1/leases/"+url.PathEscape(id), nil)
}

func (n *Node) followLease(ctx context.Context, session *connection, client *http.Client, peer Peer, le Lease, listener net.Listener) error {
	if le.DeviceInfo != nil && le.DeviceInfo.ID != "" {
		n.mu.Lock()
		deviceInfo := *le.DeviceInfo
		session.DeviceInfo = &deviceInfo
		n.mu.Unlock()
	}
	lifetime, cancelLifetime := context.WithCancel(ctx)
	defer cancelLifetime()
	address := listener.Addr().String()
	_, port, _ := net.SplitHostPort(address)
	defer func() {
		cancelLifetime()
		listener.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if imports, err := n.adapter.ListImports(cleanup); err == nil {
			for _, imported := range imports {
				if localTunnel(imported, port) {
					_ = n.adapter.Detach(cleanup, imported.Port)
				}
			}
		}
		n.releasePeer(client, peer, le.ID)
	}()
	go n.acceptLocal(lifetime, listener, peer, le.ID, client)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastRenewed := time.Now()
	canAttach := true
	for {
		state, imported, err := n.ensureImport(ctx, address, port, le, canAttach)
		n.setConnection(session, state, imported, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		round, cancel := context.WithTimeout(ctx, 4*time.Second)
		current, err := call[Lease](round, client, http.MethodPut, peer.Endpoint, "/v1/leases/"+url.PathEscape(le.ID), nil)
		cancel()
		if err == nil {
			le = current
			lastRenewed = time.Now()
			canAttach = true
			continue
		}
		unconfirmed := time.Since(lastRenewed)
		if unconfirmed >= leaseTTL || isHTTPStatus(err, http.StatusNotFound) ||
			isHTTPStatus(err, http.StatusForbidden) || isHTTPStatus(err, http.StatusUnauthorized) {
			return err
		}
		// An existing USB stream may recover through TCP retransmission. A new
		// import still requires a successful renewal and the current busid.
		canAttach = false
		if unconfirmed >= leaseRenewGrace {
			le.Online = false
		}
	}
}

func localTunnel(imported platform.Import, port string) bool {
	return (imported.RemoteHost == "127.0.0.1" || strings.EqualFold(imported.RemoteHost, "localhost")) && imported.RemotePort == port
}

func (n *Node) ensureImport(ctx context.Context, address, port string, le Lease, canAttach bool) (string, *platform.Import, error) {
	imports, err := n.adapter.ListImports(ctx)
	if err != nil {
		return "lost", nil, err
	}
	for _, imported := range imports {
		if !localTunnel(imported, port) {
			continue
		}
		if le.Online && imported.RemoteBusID == le.BusID {
			return "attached", &imported, nil
		}
		if err := n.adapter.Detach(ctx, imported.Port); err != nil {
			return "lost", nil, err
		}
	}
	if !canAttach || !le.Online || le.BusID == "" {
		return "lost", nil, nil
	}
	imported, err := n.adapter.Attach(ctx, address, le.BusID)
	if err != nil {
		return "reconnecting", nil, err
	}
	return "attached", &imported, nil
}

func (n *Node) setConnection(session *connection, state string, imported *platform.Import, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if session.State != state {
		event := state
		if state == "attached" && session.State != "connecting" {
			event = "reattached"
		}
		n.publishLocked(event, session.Device)
	}
	session.State, session.Import, session.Error = state, imported, ""
	if err != nil {
		session.Error = fmt.Sprint(err)
	}
}

func (n *Node) disconnect(ctx context.Context, id string) error {
	n.clientMu.Lock()
	defer n.clientMu.Unlock()
	n.mu.Lock()
	session := n.connections[id]
	intent, wanted := n.config.Connections[id]
	if wanted {
		delete(n.config.Connections, id)
		if err := n.saveLocked(); err != nil {
			n.config.Connections[id] = intent
			n.mu.Unlock()
			return err
		}
	}
	n.mu.Unlock()
	if session == nil {
		return nil
	}
	session.cancel()
	select {
	case <-session.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
