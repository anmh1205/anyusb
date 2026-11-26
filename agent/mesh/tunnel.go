package mesh

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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
	done := make(chan struct{})
	le.stopTunnel = stop
	le.tunnelDone = done
	n.mu.Unlock()
	defer func() {
		stop()
		n.mu.Lock()
		if le.tunnelDone == done {
			le.stopTunnel = nil
			le.tunnelDone = nil
		}
		n.mu.Unlock()
		close(done)
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
	go keepaliveLoop(ctx, stop, ws)
	_ = relay(ctx, tcp, stream)
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

type RelayResult struct {
	UploadBytes   uint64
	DownloadBytes uint64
	LastProgress  time.Time
	Reason        string
	Err           error
}

type relayPumpResult struct {
	direction string
	bytes     uint64
	side      string
	operation string
	err       error
}

const (
	relayBufferSize   = 32 << 10
	relayWriteTimeout = 15 * time.Second
)

func relay(ctx context.Context, left, right net.Conn) RelayResult {
	return relayWithProgress(ctx, left, right, nil)
}

type relayProgress func(direction string, count uint64, at time.Time)

func relayWithProgress(ctx context.Context, left, right net.Conn, progress relayProgress) RelayResult {
	var closeOnce sync.Once
	stopping := make(chan struct{})
	closeBoth := func() {
		closeOnce.Do(func() {
			close(stopping)
			_ = left.SetDeadline(time.Now())
			_ = right.SetDeadline(time.Now())
		})
	}
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	var lastProgress atomic.Int64
	done := make(chan relayPumpResult, 2)
	go func() {
		done <- relayPump(stopping, right, left, "upload", "local_backend", "peer", &lastProgress, progress)
	}()
	go func() {
		done <- relayPump(stopping, left, right, "download", "peer", "local_backend", &lastProgress, progress)
	}()
	first := <-done
	closeBoth()
	second := <-done
	result := RelayResult{Reason: relayReason(ctx, first)}
	for _, pump := range []relayPumpResult{first, second} {
		if pump.direction == "upload" {
			result.UploadBytes = pump.bytes
		} else {
			result.DownloadBytes = pump.bytes
		}
	}
	if progress := lastProgress.Load(); progress != 0 {
		result.LastProgress = time.Unix(0, progress)
	}
	if first.err != nil && !errors.Is(first.err, io.EOF) && ctx.Err() == nil {
		result.Err = first.err
	}
	return result
}

func relayPump(stopping <-chan struct{}, dst, src net.Conn, direction, source, destination string, lastProgress *atomic.Int64, progress relayProgress) relayPumpResult {
	result := relayPumpResult{direction: direction}
	buffer := make([]byte, relayBufferSize)
	for {
		read, err := src.Read(buffer)
		if read > 0 {
			written := 0
			for written < read {
				_ = dst.SetWriteDeadline(time.Now().Add(relayWriteTimeout))
				select {
				case <-stopping:
					return result
				default:
				}
				count, writeErr := dst.Write(buffer[written:read])
				_ = dst.SetWriteDeadline(time.Time{})
				if count > 0 {
					written += count
					result.bytes += uint64(count)
					at := time.Now()
					for previous := lastProgress.Load(); previous < at.UnixNano(); previous = lastProgress.Load() {
						if lastProgress.CompareAndSwap(previous, at.UnixNano()) {
							break
						}
					}
					if progress != nil {
						progress(direction, uint64(count), at)
					}
				}
				if writeErr != nil {
					result.side, result.operation, result.err = destination, "write", writeErr
					return result
				}
				if count == 0 {
					result.side, result.operation, result.err = destination, "write", io.ErrNoProgress
					return result
				}
			}
		}
		if err != nil {
			result.side, result.operation, result.err = source, "read", err
			return result
		}
	}
}

func relayReason(ctx context.Context, result relayPumpResult) string {
	if ctx.Err() != nil {
		return "canceled"
	}
	if result.err == nil || errors.Is(result.err, io.EOF) {
		return "eof"
	}
	var networkError net.Error
	if errors.As(result.err, &networkError) && networkError.Timeout() {
		return "timeout"
	}
	if result.side == "local_backend" {
		return "local_backend_failure"
	}
	if result.side == "peer" {
		return "peer_failure"
	}
	return result.direction + "_failure"
}

type TunnelStats struct {
	UploadBytes      uint64     `json:"upload_bytes"`
	DownloadBytes    uint64     `json:"download_bytes"`
	LastProgress     *time.Time `json:"last_progress,omitempty"`
	ReconnectCount   int        `json:"reconnect_count"`
	LeaseAgeMS       int64      `json:"lease_age_ms,omitempty"`
	DisconnectReason string     `json:"disconnect_reason,omitempty"`
}

type Connection struct {
	ID           string             `json:"id"`
	Peer         string             `json:"peer"`
	Device       string             `json:"device"`
	State        string             `json:"state"`
	Error        string             `json:"error,omitempty"`
	RiskAccepted bool               `json:"risk_accepted,omitempty"`
	DeviceInfo   *platform.Device   `json:"device_info,omitempty"`
	Import       *platform.Import   `json:"import,omitempty"`
	Phase        string             `json:"phase,omitempty"`
	Attempt      int                `json:"attempt,omitempty"`
	NextRetryAt  *time.Time         `json:"next_retry_at,omitempty"`
	LastFailure  *ConnectionFailure `json:"last_failure,omitempty"`
	Stats        TunnelStats        `json:"stats"`
}

type connection struct {
	Connection
	statsMu      sync.Mutex
	liveStats    TunnelStats
	cancel       context.CancelFunc
	done         chan struct{}
	wake         chan struct{}
	attachedOnce bool
}

func (c *connection) statsSnapshot() TunnelStats {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	stats := c.liveStats
	if stats.LastProgress != nil {
		ts := *stats.LastProgress
		stats.LastProgress = &ts
	}
	return stats
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
				previous := intent
				intent.RiskAccepted = true
				n.config.Connections[intent.ID] = intent
				if err := n.saveLocked(); err != nil {
					n.config.Connections[intent.ID] = previous
					return Connection{}, err
				}
			}
			if session := n.connections[intent.ID]; session != nil {
				session.RiskAccepted = intent.RiskAccepted
				select {
				case session.wake <- struct{}{}:
				default:
				}
				view := session.Connection
				view.Stats = session.statsSnapshot()
				return view, nil
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
	session := &connection{Connection: view, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
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
	clock := n.clock
	if clock == nil {
		clock = realClock{}
	}
	retryPolicy := n.retryPolicy
	if retryPolicy.Base <= 0 || retryPolicy.Max <= 0 {
		retryPolicy = DefaultRetryPolicy()
	}
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		operation := "peer_lookup"
		n.setConnectionPhase(session, operation)
		peer, err := n.findPeer(session.Peer)
		if err == nil {
			client := n.peerClient(peer.ID)
			n.mu.Lock()
			riskAccepted := session.RiskAccepted
			n.mu.Unlock()
			operation = "lease"
			n.setConnectionPhase(session, operation)
			attemptCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			le, acquireErr := call[Lease](attemptCtx, client, http.MethodPost, peer.Endpoint, "/v1/leases", struct {
				Device      string `json:"device"`
				ConfirmRisk bool   `json:"confirm_risk"`
			}{session.Device, riskAccepted})
			cancel()
			if acquireErr == nil {
				operation = "tunnel"
				n.setConnectionPhase(session, operation)
				listener, listenErr := net.Listen("tcp4", "127.0.0.1:0")
				if listenErr == nil {
					var stable bool
					stable, operation, err = n.followLease(ctx, session, client, peer, le, listener)
					if stable {
						attempt = 0
					}
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
		failure := newConnectionFailure(operation, err, clock.Now())
		if failure != nil && !failure.Retryable {
			n.setConnectionFailure(session, "blocked", failure, nil, attempt)
			select {
			case <-ctx.Done():
				return
			case <-session.wake:
				attempt = 0
				continue
			}
		}
		attempt++
		delay := retryPolicy.Delay(attempt)
		nextRetry := clock.Now().Add(delay)
		n.setConnectionFailure(session, "reconnecting", failure, &nextRetry, attempt)
		if clock.Wait(ctx, delay) != nil {
			return
		}
	}
}

func keepaliveLoop(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn) {
	ticker := time.NewTicker(8 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			err := ws.Ping(pingCtx)
			stop()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

func (n *Node) acceptLocal(ctx context.Context, session *connection, listener net.Listener, peer Peer, leaseID string, client *http.Client, onTunnelEnd chan<- struct{}) {
	// The virtual controller is the only expected local client. The host accepts one active tunnel per lease.
	for {
		tcp, err := listener.Accept()
		if err != nil {
			return
		}
		func() {
			defer tcp.Close()
			tunnelCtx, cancelTunnel := context.WithCancel(ctx)
			defer cancelTunnel()
			target := peerURL(peer.Endpoint, "/v1/tunnel/"+url.PathEscape(leaseID))
			ws, _, err := websocket.Dial(tunnelCtx, target, &websocket.DialOptions{HTTPClient: client, Subprotocols: []string{tunnelProtocol}})
			if err != nil {
				return
			}
			defer ws.CloseNow()
			if ws.Subprotocol() == tunnelProtocol {
				go keepaliveLoop(tunnelCtx, cancelTunnel, ws)
				result := relayWithProgress(tunnelCtx, tcp, websocket.NetConn(tunnelCtx, ws, websocket.MessageBinary),
					func(direction string, count uint64, at time.Time) {
						n.recordRelayProgress(session, direction, count, at)
					})
				n.finishRelay(session, result)
			}
		}()
		if onTunnelEnd != nil {
			select {
			case onTunnelEnd <- struct{}{}:
			default:
			}
		}
	}
}

func (n *Node) releasePeer(client *http.Client, peer Peer, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = call[struct{}](ctx, client, http.MethodDelete, peer.Endpoint, "/v1/leases/"+url.PathEscape(id), nil)
}

func (n *Node) followLease(ctx context.Context, session *connection, client *http.Client, peer Peer, le Lease, listener net.Listener) (bool, string, error) {
	n.updateConnectionDevice(session, le.DeviceInfo)
	leaseStarted := time.Now()
	stable := false
	lifetime, cancelLifetime := context.WithCancel(ctx)
	address := listener.Addr().String()
	_, port, _ := net.SplitHostPort(address)
	tunnelDone := make(chan struct{})
	tunnelWake := make(chan struct{}, 1)
	go func() {
		defer close(tunnelDone)
		n.acceptLocal(lifetime, session, listener, peer, le.ID, client, tunnelWake)
	}()
	defer func() {
		cancelLifetime()
		listener.Close()
		<-tunnelDone
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
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastRenewed := time.Now()
	canAttach := true
	var imported *platform.Import
	var fresh bool
	forceCheck := true
	readyTicks := 0
	for {
		if forceCheck || imported == nil || imported.Readiness != "ready" || readyTicks%2 == 0 {
			n.setConnectionPhaseIfDetached(session, "usbip_attach")
			var err error
			imported, fresh, err = n.ensureImport(ctx, address, port, le, canAttach)
			if err != nil {
				return stable, "usbip_attach", err
			}
			state := "lost"
			if imported != nil {
				state = "attached"
			}
			n.setConnection(session, state, imported, fresh)
			forceCheck = false
		}
		readyTicks++
		n.setLeaseAge(session, time.Since(leaseStarted))
		select {
		case <-ctx.Done():
			return stable, "lease", ctx.Err()
		case <-tunnelWake:
			forceCheck = true
			if time.Since(lastRenewed) < 900*time.Millisecond {
				continue
			}
		case <-ticker.C:
		}
		round, cancel := context.WithTimeout(ctx, 4*time.Second)
		current, err := call[Lease](round, client, http.MethodPut, peer.Endpoint, "/v1/leases/"+url.PathEscape(le.ID), nil)
		cancel()
		if err == nil {
			le = current
			n.updateConnectionDevice(session, le.DeviceInfo)
			lastRenewed = time.Now()
			if imported != nil && le.Online && imported.RemoteBusID == le.BusID {
				stable = true
				n.markConnectionStable(session)
			}
			canAttach = true
			continue
		}
		unconfirmed := time.Since(lastRenewed)
		if unconfirmed >= leaseTTL || isHTTPStatus(err, http.StatusNotFound) ||
			isHTTPStatus(err, http.StatusForbidden) || isHTTPStatus(err, http.StatusUnauthorized) {
			return stable, "lease", err
		}
		// An existing USB stream may recover through TCP retransmission. A new
		// import still requires a successful renewal and the current busid.
		canAttach = false
		if unconfirmed >= leaseRenewGrace {
			le.Online = false
		}
	}
}

func (n *Node) updateConnectionDevice(session *connection, device *platform.Device) {
	if device == nil || device.ID == "" {
		return
	}
	copy := *device
	n.mu.Lock()
	session.DeviceInfo = &copy
	n.mu.Unlock()
}

func localTunnel(imported platform.Import, port string) bool {
	return (imported.RemoteHost == "127.0.0.1" || strings.EqualFold(imported.RemoteHost, "localhost")) && imported.RemotePort == port
}

func (n *Node) ensureImport(ctx context.Context, address, port string, le Lease, canAttach bool) (*platform.Import, bool, error) {
	imports, err := n.adapter.ListImports(ctx)
	if err != nil {
		return nil, false, err
	}
	for _, imported := range imports {
		if !localTunnel(imported, port) {
			continue
		}
		if le.Online && imported.RemoteBusID == le.BusID {
			return &imported, false, nil
		}
		if err := n.adapter.Detach(ctx, imported.Port); err != nil {
			return nil, false, err
		}
	}
	if !canAttach || !le.Online || le.BusID == "" {
		return nil, false, nil
	}
	imported, err := n.adapter.Attach(ctx, address, le.BusID)
	if err != nil {
		return nil, false, err
	}
	return &imported, true, nil
}

func (n *Node) setConnection(session *connection, state string, imported *platform.Import, fresh bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	previousPhase := session.Phase
	if state == "attached" && (fresh || session.State != state) {
		if session.attachedOnce {
			session.statsMu.Lock()
			session.liveStats.ReconnectCount++
			session.statsMu.Unlock()
			n.publishLocked("reattached", session.Device)
		} else {
			session.attachedOnce = true
			n.publishLocked("attached", session.Device)
		}
	} else if session.State != state {
		n.publishLocked(state, session.Device)
	}
	session.State, session.Import, session.Error, session.NextRetryAt = state, imported, "", nil
	switch {
	case state == "attached" && imported != nil && imported.Readiness == "ready":
		session.Phase = "ready"
	case state == "attached":
		session.Phase = "os_enumeration"
	case state == "reconnecting":
		session.Phase = "usbip_attach"
	case state == "lost":
		session.Phase = "tunnel"
	}
	if session.Phase == "ready" && previousPhase != "ready" {
		n.publishLocked("ready", session.Device)
	}
}

func (n *Node) setConnectionPhase(session *connection, phase string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	session.Phase = phase
	session.NextRetryAt = nil
}

func (n *Node) setConnectionPhaseIfDetached(session *connection, phase string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if session.Import == nil {
		session.Phase = phase
	}
}

func (n *Node) setConnectionFailure(session *connection, state string, failure *ConnectionFailure, next *time.Time, attempt int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	session.State, session.Phase, session.Attempt, session.NextRetryAt, session.LastFailure = state, "backoff", attempt, next, failure
	session.Import = nil
	session.statsMu.Lock()
	session.liveStats.LeaseAgeMS = 0
	session.statsMu.Unlock()
	session.Error = ""
	if failure != nil {
		session.Error = failure.Message
		if !failure.Retryable {
			session.Phase = "blocked"
		}
		n.publishEventLocked(Event{Type: state, Device: session.Device, Code: failure.Code, Operation: failure.Operation, Attempt: attempt})
	} else {
		n.publishLocked(state, session.Device)
	}
}

func (n *Node) recordRelayProgress(session *connection, direction string, count uint64, at time.Time) {
	session.statsMu.Lock()
	defer session.statsMu.Unlock()
	if direction == "upload" {
		session.liveStats.UploadBytes += count
	} else {
		session.liveStats.DownloadBytes += count
	}
	if session.liveStats.LastProgress == nil || at.After(*session.liveStats.LastProgress) {
		session.liveStats.LastProgress = &at
	}
}

func (n *Node) finishRelay(session *connection, result RelayResult) {
	session.statsMu.Lock()
	defer session.statsMu.Unlock()
	session.liveStats.DisconnectReason = result.Reason
}

func (n *Node) setLeaseAge(session *connection, age time.Duration) {
	session.statsMu.Lock()
	defer session.statsMu.Unlock()
	session.liveStats.LeaseAgeMS = age.Milliseconds()
}

func (n *Node) markConnectionStable(session *connection) {
	n.mu.Lock()
	defer n.mu.Unlock()
	session.Attempt = 0
	session.NextRetryAt = nil
	session.LastFailure = nil
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
