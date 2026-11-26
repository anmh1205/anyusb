package mesh

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
	platformmock "github.com/anmh1205/anyusb/agent/platform/mock"
)

type testAdapter struct {
	mu        sync.Mutex
	device    platform.Device
	imported  *platform.Import
	socket    net.Conn
	attachErr error
}

func (a *testAdapter) ListLocalDevices(context.Context) ([]platform.Device, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.device.ID == "" {
		return nil, nil
	}
	return []platform.Device{a.device}, nil
}

func (a *testAdapter) LocalSerialPorts(context.Context, string) ([]string, error) {
	return []string{"test-serial"}, nil
}

func (a *testAdapter) Export(_ context.Context, id string) (platform.Device, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id != a.device.BusID {
		return platform.Device{}, platform.ErrNotFound
	}
	a.device.Exported = true
	return a.device, nil
}

func (a *testAdapter) Unexport(_ context.Context, id string) (platform.Device, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id != a.device.BusID {
		return platform.Device{}, platform.ErrNotFound
	}
	a.device.Exported = false
	return a.device, nil
}

func (a *testAdapter) ListRemote(context.Context, string) ([]platform.RemoteDevice, error) {
	return nil, nil
}
func (a *testAdapter) ListImports(context.Context) ([]platform.Import, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.imported == nil {
		return nil, nil
	}
	return []platform.Import{*a.imported}, nil
}

func packet(bus string) (request [40]byte) {
	binary.BigEndian.PutUint16(request[:2], 0x0111)
	binary.BigEndian.PutUint16(request[2:4], 0x8003)
	copy(request[8:], bus)
	return request
}

func (a *testAdapter) Attach(ctx context.Context, endpoint, bus string) (platform.Import, error) {
	if a.attachErr != nil {
		return platform.Import{}, a.attachErr
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return platform.Import{}, err
	}
	request := packet(bus)
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(request[:]); err != nil {
		conn.Close()
		return platform.Import{}, err
	}
	var reply [8]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil || binary.BigEndian.Uint16(reply[2:4]) != 0x0003 || binary.BigEndian.Uint32(reply[4:]) != 0 {
		conn.Close()
		return platform.Import{}, errors.New("USB/IP test backend failed to acknowledge the import")
	}
	_ = conn.SetDeadline(time.Time{})
	host, port, _ := net.SplitHostPort(endpoint)
	result := platform.Import{Port: 1, RemoteHost: host, RemotePort: port, RemoteBusID: bus, State: "attached"}
	a.mu.Lock()
	a.imported, a.socket = &result, conn
	a.mu.Unlock()
	go func() {
		io.Copy(io.Discard, conn)
		conn.Close()
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.socket == conn {
			a.imported, a.socket = nil, nil
		}
	}()
	return result, nil
}

type retryTestClock struct {
	waits chan time.Duration
	step  chan struct{}
}

func (clock retryTestClock) Now() time.Time { return time.Unix(1, 0) }

func (clock retryTestClock) Wait(ctx context.Context, delay time.Duration) error {
	select {
	case clock.waits <- delay:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-clock.step:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestManagedNativeAttachFailureBacksOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	hostAdapter, err := platformmock.New(ctx, "basic")
	if err != nil {
		t.Fatal(err)
	}
	host, endpoint := nodeForTest(t, ctx, hostAdapter, hostAdapter.BackendAddress())
	client, _ := nodeForTest(t, ctx, &testAdapter{attachErr: platform.ErrUnavailable}, "127.0.0.1:3240")
	clock := retryTestClock{waits: make(chan time.Duration), step: make(chan struct{})}
	client.clock = clock
	client.retryPolicy = RetryPolicy{Base: time.Second, Max: time.Minute}
	const device = "mock-port-1"
	if err := host.setShared(ctx, device, true); err != nil {
		t.Fatal(err)
	}
	invite, err := host.newInvite()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := client.pair(ctx, endpoint, invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := client.connect(ctx, peer, device)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []time.Duration{time.Second, 2 * time.Second} {
		select {
		case got := <-clock.waits:
			if got != want {
				t.Fatalf("retry delay = %s, want %s", got, want)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		views := client.connectionViews()
		if len(views) != 1 || views[0].Phase != "backoff" || views[0].Import != nil || views[0].LastFailure == nil ||
			views[0].LastFailure.Operation != "usbip_attach" || views[0].LastFailure.Code != "backend_unavailable" {
			t.Fatalf("native attach failure = %+v", views)
		}
		if want == time.Second {
			// The host drains released leases on its reconciliation tick.
			host.reconcile()
			clock.step <- struct{}{}
		}
	}
	if err := client.disconnect(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
}

func (a *testAdapter) Detach(_ context.Context, _ int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.socket != nil {
		a.socket.Close()
	}
	a.imported, a.socket = nil, nil
	return nil
}

func (a *testAdapter) WatchHotplug(ctx context.Context) (<-chan platform.HotplugEvent, error) {
	return platform.PollHotplug(ctx, a.ListLocalDevices)
}
func (a *testAdapter) Doctor(context.Context) []platform.Check { return nil }

func nodeForTest(t *testing.T, ctx context.Context, adapter platform.Adapter, backend string) (*Node, string) {
	t.Helper()
	n, err := New(ctx, adapter, t.TempDir(), "AnyUSB test", "", backend)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(n.PeerHandler())
	server.TLS = n.TLSConfig()
	server.StartTLS()
	t.Cleanup(server.Close)
	return n, server.Listener.Addr().String()
}

func TestMockNetworkDeviceThroughManagedConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hostAdapter, err := platformmock.New(ctx, "cdc-ncm")
	if err != nil {
		t.Fatal(err)
	}
	clientAdapter, err := platformmock.New(ctx, "basic")
	if err != nil {
		t.Fatal(err)
	}
	host, endpoint := nodeForTest(t, ctx, hostAdapter, hostAdapter.BackendAddress())
	client, _ := nodeForTest(t, ctx, clientAdapter, clientAdapter.BackendAddress())
	const device = "mock-port-1"
	if err := host.setShared(ctx, device, true); err != nil {
		t.Fatal(err)
	}
	invite, err := host.newInvite()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := client.pair(ctx, endpoint, invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.connect(ctx, peer, device); err != nil {
		t.Fatal(err)
	}
	awaitFailure := func(code string) {
		t.Helper()
		requireEventually(t, code, func() bool {
			views := client.connectionViews()
			return len(views) == 1 && views[0].Import == nil && views[0].LastFailure != nil && views[0].LastFailure.Code == code
		})
	}
	awaitFailure("risk_required")
	if _, err := client.connectWithPolicy(ctx, peer, device, true); err != nil {
		t.Fatal(err)
	}
	requireEventually(t, "mock CDC connection becomes ready with a network binding", func() bool {
		views := client.connectionViews()
		if len(views) != 1 || views[0].Phase != "ready" || views[0].Import == nil {
			return false
		}
		imp := views[0].Import
		return len(imp.Bindings) == 1 && imp.Bindings[0].Kind == "network" && len(imp.SerialPorts) == 0
	})
	if _, err := host.forceRelease(device); err != nil {
		t.Fatal(err)
	}
	awaitFailure("force_release_cooldown")
	if err := host.revoke(client.id); err != nil {
		t.Fatal(err)
	}
	awaitFailure("peer_revoked")
}

func TestMockTunnelLossCountsReattachmentBetweenPolls(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hostAdapter, err := platformmock.New(ctx, "network-loss")
	if err != nil {
		t.Fatal(err)
	}
	clientAdapter, err := platformmock.New(ctx, "basic")
	if err != nil {
		t.Fatal(err)
	}
	host, endpoint := nodeForTest(t, ctx, hostAdapter, hostAdapter.BackendAddress())
	client, _ := nodeForTest(t, ctx, clientAdapter, clientAdapter.BackendAddress())
	const device = "mock-port-1"
	if err := host.setShared(ctx, device, true); err != nil {
		t.Fatal(err)
	}
	invite, err := host.newInvite()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := client.pair(ctx, endpoint, invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.connect(ctx, peer, device)
	if err != nil {
		t.Fatal(err)
	}
	firstPort := 0
	requireEventually(t, "first mock import ready", func() bool {
		views := client.connectionViews()
		if len(views) != 1 || views[0].Phase != "ready" {
			return false
		}
		firstPort = views[0].Import.Port
		return true
	})
	requireEventually(t, "mock relay reattached with one reconnect counted", func() bool {
		views := client.connectionViews()
		return len(views) == 1 && views[0].Phase == "ready" && views[0].Import.Port != firstPort &&
			views[0].Stats.ReconnectCount == 1 && views[0].Stats.DisconnectReason != ""
	})
	if err := client.disconnect(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
}

func requireEventually(t *testing.T, description string, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out:", description)
}

func TestManagedRiskPolicyRequiresAndPersistsAcceptance(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	storage := platform.EnrichDevice(platform.Device{
		ID: "disk", BusID: "1-2", VendorID: "1209", ProductID: "0001", Serial: "disposable",
		Incarnation: "disk-1", Functions: []platform.USBFunction{{ID: "1:0.0", Class: "08"}},
	})
	host, _ := nodeForTest(t, ctx, &testAdapter{device: storage}, "127.0.0.1:3240")
	host.mu.Lock()
	host.config.Peers["client"] = Peer{ID: "client", Name: "client"}
	host.mu.Unlock()
	if err := host.setShared(ctx, storage.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := host.acquireWithPolicy(ctx, "client", storage.ID, "usbip", false); !errors.Is(err, platform.ErrInvalid) {
		t.Fatalf("unconfirmed storage lease = %v", err)
	}
	le, err := host.acquireWithPolicy(ctx, "client", storage.ID, "usbip", true)
	if err != nil || le.DeviceInfo.Risk != "storage" || le.DeviceInfo.ReconnectPolicy != platform.ReconnectConfirmed {
		t.Fatalf("confirmed storage lease = %+v, %v", le, err)
	}
	if _, err := host.renew("client", le.ID); err != nil {
		t.Fatalf("confirmed lease did not renew: %v", err)
	}
}

func TestManagedNCMRequiresNetworkRiskAcceptance(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ncm := platform.EnrichDevice(platform.Device{
		ID: "ncm", BusID: "1-3", VendorID: "1209", ProductID: "0002", Serial: "network",
		Incarnation: "ncm-1", Functions: []platform.USBFunction{
			{Configuration: 1, Interface: 0, Class: "02", SubClass: "0d"},
			{Configuration: 1, Interface: 1, Class: "0a"},
		},
	})
	host, _ := nodeForTest(t, ctx, &testAdapter{device: ncm}, "127.0.0.1:3240")
	host.mu.Lock()
	host.config.Peers["client"] = Peer{ID: "client", Name: "client"}
	host.mu.Unlock()
	if err := host.setShared(ctx, ncm.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := host.acquireWithPolicy(ctx, "client", ncm.ID, "usbip", false); !errors.Is(err, platform.ErrInvalid) || failureCode(err) != "risk_required" {
		t.Fatalf("unconfirmed NCM lease = %v (%q)", err, failureCode(err))
	}
	lease, err := host.acquireWithPolicy(ctx, "client", ncm.ID, "usbip", true)
	if err != nil || lease.DeviceInfo.Risk != "network" || lease.DeviceInfo.SharePolicy != platform.ShareConfirm ||
		lease.DeviceInfo.ReconnectPolicy != platform.ReconnectConfirmed {
		t.Fatalf("confirmed NCM lease = %+v, %v", lease, err)
	}
}

func TestForceReleaseFencesPeerAndDevice(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	device := platform.EnrichDevice(platform.Device{
		ID: "board", BusID: "1-4", VendorID: "303a", ProductID: "1001", Serial: "board",
		Incarnation: "board-1", Functions: []platform.USBFunction{{Class: "ff"}},
	})
	host, _ := nodeForTest(t, ctx, &testAdapter{device: device}, "127.0.0.1:3240")
	host.mu.Lock()
	host.config.Peers["client-a"] = Peer{ID: "client-a", Name: "client A"}
	host.config.Peers["client-b"] = Peer{ID: "client-b", Name: "client B"}
	host.mu.Unlock()
	if err := host.setShared(ctx, device.ID, true); err != nil {
		t.Fatal(err)
	}
	lease, err := host.acquireWithPolicy(ctx, "client-a", device.ID, "usbip", false)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := host.forceRelease(device.ID)
	if err != nil || fence.Peer != "client-a" || fence.Device != device.ID || fence.Generation <= lease.Generation ||
		time.Until(fence.Until) < 55*time.Second {
		t.Fatalf("force-release fence = %+v, %v", fence, err)
	}
	if _, err := host.renew("client-a", lease.ID); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("fenced lease renewed: %v", err)
	}
	if _, err := host.acquireWithPolicy(ctx, "client-a", device.ID, "usbip", false); !errors.Is(err, platform.ErrConflict) || failureCode(err) != "force_release_cooldown" {
		t.Fatalf("fenced peer reacquire = %v (%q)", err, failureCode(err))
	}
	host.reconcile()
	if _, err := host.acquireWithPolicy(ctx, "client-b", device.ID, "usbip", false); err != nil {
		t.Fatalf("different peer was blocked by another peer's fence: %v", err)
	}
	host.mu.Lock()
	persisted := host.config.Fences[leaseFenceKey("client-a", device.ID)]
	host.mu.Unlock()
	if persisted.Generation != fence.Generation || !persisted.Until.Equal(fence.Until) {
		t.Fatalf("persisted fence = %+v", persisted)
	}
}

func TestManagedSharingRejectsUSBHub(t *testing.T) {
	hub := platform.EnrichDevice(platform.Device{ID: "hub", BusID: "1-1", Product: "Generic USB Hub", Incarnation: "hub-1"})
	node, _ := nodeForTest(t, t.Context(), &testAdapter{device: hub}, "127.0.0.1:3240")
	if err := node.setShared(t.Context(), hub.ID, true); !errors.Is(err, platform.ErrInvalid) {
		t.Fatalf("hub share = %v", err)
	}
}

func TestManagedLeaseRejectsPersistedUSBHubShare(t *testing.T) {
	hub := platform.EnrichDevice(platform.Device{
		ID: "hub", BusID: "1-1", VendorID: "1209", ProductID: "0001", Serial: "hub",
		Product: "Generic USB Hub", Incarnation: "hub-1",
	})
	adapter := &testAdapter{device: hub}
	node := &Node{
		ctx: t.Context(), adapter: adapter, leases: make(map[string]*lease), inventory: make(map[string]platform.Device),
		config: config{Peers: map[string]Peer{"client": {ID: "client"}}, Shares: map[string][]descriptor{
			hub.ID: {describe(hub)},
		}, Connections: make(map[string]connectionIntent)},
	}
	if _, err := node.acquireWithPolicy(t.Context(), "client", hub.ID, "usbip", true); !errors.Is(err, platform.ErrInvalid) {
		t.Fatalf("persisted hub lease = %v", err)
	}
	adapter.mu.Lock()
	exported := adapter.device.Exported
	adapter.mu.Unlock()
	if exported {
		t.Fatal("rejected persisted hub was exported")
	}
}

func TestManagedLeaseRechecksPolicyWhenDeviceProfileChanges(t *testing.T) {
	device := platform.EnrichDevice(platform.Device{
		ID: "device", BusID: "1-2", VendorID: "1209", ProductID: "0002", Serial: "variant", Incarnation: "same",
		Functions: []platform.USBFunction{{ID: "1:0.0", Class: "ff"}},
	})
	adapter := &testAdapter{device: device}
	node := &Node{
		ctx: t.Context(), adapter: adapter, leases: make(map[string]*lease), inventory: make(map[string]platform.Device),
		config: config{Peers: map[string]Peer{"client": {ID: "client"}}, Shares: map[string][]descriptor{
			device.ID: {describe(device)},
		}, Connections: make(map[string]connectionIntent)},
	}
	if _, err := node.acquireWithPolicy(t.Context(), "client", device.ID, "usbip", false); err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	adapter.device = platform.EnrichDevice(platform.Device{
		ID: device.ID, BusID: device.BusID, VendorID: device.VendorID, ProductID: device.ProductID,
		Serial: device.Serial, Incarnation: device.Incarnation, Exported: true,
		Functions: []platform.USBFunction{{ID: "1:0.0", Class: "08"}},
	})
	adapter.mu.Unlock()
	node.reconcile()
	if len(node.leases) != 0 {
		t.Fatalf("high-risk profile change kept lease: %+v", node.leases)
	}
	adapter.mu.Lock()
	exported := adapter.device.Exported
	adapter.mu.Unlock()
	if exported {
		t.Fatal("high-risk profile change kept the device exported")
	}
}

func TestProfileProbeIsExplicitAndMatchesDeviceProfile(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	device := platform.Device{Serial: "DAP123", Profiles: []string{"debug-probe", "cmsis-dap"}}
	node := &Node{connections: map[string]*connection{
		"connection": {Connection: Connection{ID: "connection", State: "attached", DeviceInfo: &device, Import: &platform.Import{Port: 1}}},
	}}
	result, err := node.probeConnection(t.Context(), "connection", "CMSIS-DAP")
	if err != nil || result.OK || result.Error == "" {
		t.Fatalf("missing optional profile tool = %+v, %v", result, err)
	}
	if _, err := node.probeConnection(t.Context(), "connection", "jlink"); !errors.Is(err, platform.ErrInvalid) {
		t.Fatalf("mismatched profile probe = %v", err)
	}
}

func TestManagedPairLeaseAndReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	backend, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	requests := make(chan string, 16)
	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var request [40]byte
				if _, err := io.ReadFull(conn, request[:]); err != nil {
					return
				}
				switch {
				case validImport(request, "1-4"):
					requests <- "1-4"
				case validImport(request, "2-9"):
					requests <- "2-9"
				default:
					return
				}
				conn.Write([]byte{0x01, 0x11, 0x00, 0x03, 0x00, 0x00, 0x00, 0x00})
				io.Copy(io.Discard, conn)
			}()
		}
	}()
	hostAdapter := &testAdapter{device: platform.Device{ID: "board", BusID: "1-4", VendorID: "303a", ProductID: "1001", Serial: "lab", Incarnation: "first"}}
	host, address := nodeForTest(t, ctx, hostAdapter, backend.Addr().String())
	client, _ := nodeForTest(t, ctx, &testAdapter{}, backend.Addr().String())
	second, _ := nodeForTest(t, ctx, &testAdapter{}, backend.Addr().String())
	unpaired := client.peerClient(host.id)
	defer unpaired.CloseIdleConnections()
	if _, err := call[[]SharedDevice](ctx, unpaired, http.MethodGet, address, "/v1/shared", nil); !isHTTPStatus(err, http.StatusUnauthorized) {
		t.Fatalf("unpaired access = %v", err)
	}
	if err := host.setShared(ctx, "board", true); err != nil {
		t.Fatal(err)
	}
	invite, err := host.newInvite()
	if err != nil {
		t.Fatal(err)
	}
	if len(invite.Code) != 4 {
		t.Fatalf("invite code = %q, want 4 digits", invite.Code)
	}
	wrong := "0000"
	if wrong == invite.Code {
		wrong = "0001"
	}
	if _, err := client.pair(ctx, address, wrong); !isHTTPStatus(err, http.StatusForbidden) {
		t.Fatalf("wrong invite = %v", err)
	}
	peer, err := client.pair(ctx, address, invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.pair(ctx, address, invite.Code); !isHTTPStatus(err, http.StatusForbidden) {
		t.Fatalf("reused invite = %v", err)
	}
	invite, err = host.newInvite()
	if err != nil {
		t.Fatal(err)
	}
	competitor, err := second.pair(ctx, address, invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.connect(ctx, peer, "board")
	if err != nil {
		t.Fatal(err)
	}
	requireEventually(t, "client attached on original busid", func() bool {
		views := client.connectionViews()
		return len(views) == 1 && views[0].Import != nil && views[0].Import.RemoteBusID == "1-4"
	})
	competing, err := second.connect(ctx, competitor, "board")
	if err != nil {
		t.Fatal(err)
	}
	requireEventually(t, "second USB/IP client reports the busy lease", func() bool {
		views := second.connectionViews()
		return len(views) == 1 && views[0].Error != ""
	})
	if err := second.disconnect(ctx, competing.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := host.BeginESP(ctx, client.id, "board", ""); !errors.Is(err, platform.ErrConflict) {
		t.Fatalf("ESP while USB/IP attached = %v", err)
	}
	hostAdapter.mu.Lock()
	hostAdapter.device.BusID, hostAdapter.device.Incarnation, hostAdapter.device.Exported = "2-9", "returned", false
	hostAdapter.mu.Unlock()
	requireEventually(t, "client recovered on new busid", func() bool {
		views := client.connectionViews()
		return len(views) == 1 && views[0].Import != nil && views[0].Import.RemoteBusID == "2-9"
	})
	for _, want := range []string{"1-4", "2-9"} {
		select {
		case got := <-requests:
			if got != want {
				t.Fatalf("backend received %s, want %s", got, want)
			}
		default:
			t.Fatalf("backend never received %s", want)
		}
	}
	if err := client.disconnect(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	requireEventually(t, "host released managed export", func() bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		return len(host.leases) == 0
	})
	port, release, err := host.BeginESP(ctx, client.id, "board", "")
	if err != nil || port != "test-serial" {
		t.Fatalf("host serial lease = %s, %v", port, err)
	}
	defer release()
	if _, err := host.RawShare(ctx, "2-9", true); !errors.Is(err, platform.ErrConflict) {
		t.Fatalf("raw export bypassed the ESP lease: %v", err)
	}
	competing, err = second.connect(ctx, competitor, "board")
	if err != nil {
		t.Fatal(err)
	}
	requireEventually(t, "USB/IP retry waits while ESP owns the device", func() bool {
		views := second.connectionViews()
		return len(views) == 1 && views[0].Error != ""
	})
	if err := second.disconnect(ctx, competing.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTunnelImportHeader(t *testing.T) {
	good := packet("1-4")
	if !validImport(good, "1-4") || validImport(good, "1-5") {
		t.Fatal("busid authorization failed")
	}
	good[3] = 5
	if validImport(good, "1-4") {
		t.Fatal("accepted a non-import request")
	}
}

func TestConnectionIntentSurvivesAgentRestart(t *testing.T) {
	dir := t.TempDir()
	peer := Peer{ID: strings.Repeat("a", 64), Name: "offline peer", Endpoint: "127.0.0.1:1"}

	firstContext, stopFirst := context.WithCancel(t.Context())
	first, err := New(firstContext, &testAdapter{}, dir, "first", "", "127.0.0.1:3240")
	if err != nil {
		t.Fatal(err)
	}
	first.mu.Lock()
	if err := first.addPeerLocked(peer); err != nil {
		first.mu.Unlock()
		t.Fatal(err)
	}
	first.mu.Unlock()
	created, err := first.connectWithPolicy(t.Context(), peer, "board", true)
	if err != nil {
		t.Fatal(err)
	}
	first.mu.Lock()
	oldSession := first.connections[created.ID]
	first.mu.Unlock()
	if oldSession == nil {
		t.Fatal("connection worker was not created")
	}
	stopFirst()
	select {
	case <-oldSession.done:
	case <-time.After(3 * time.Second):
		t.Fatal("first connection worker did not stop")
	}

	secondContext, stopSecond := context.WithCancel(t.Context())
	defer stopSecond()
	second, err := New(secondContext, &testAdapter{}, dir, "second", "", "127.0.0.1:3240")
	if err != nil {
		t.Fatal(err)
	}
	restored := second.connectionViews()
	if len(restored) != 1 || restored[0].ID != created.ID || restored[0].Peer != peer.ID || restored[0].Device != "board" || !restored[0].RiskAccepted {
		t.Fatalf("restored connections = %+v", restored)
	}
	if err := second.disconnect(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	if views := second.connectionViews(); len(views) != 0 {
		t.Fatalf("disconnected intent still active: %+v", views)
	}

	state, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved config
	if err := json.Unmarshal(state, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Connections) != 0 {
		t.Fatalf("disconnected intent still persisted: %+v", saved.Connections)
	}
}

func TestRiskAcceptanceRollsBackWhenPersistenceFails(t *testing.T) {
	peer := Peer{ID: "peer", Endpoint: "127.0.0.1:1"}
	intent := connectionIntent{ID: "connection", Peer: peer.ID, Device: "disk"}
	node := &Node{
		ctx: t.Context(), dir: filepath.Join(t.TempDir(), "missing"), connections: make(map[string]*connection),
		config: config{Peers: map[string]Peer{peer.ID: peer}, Shares: make(map[string][]descriptor),
			Connections: map[string]connectionIntent{intent.ID: intent}},
	}
	if _, err := node.connectWithPolicy(t.Context(), peer, intent.Device, true); err == nil {
		t.Fatal("risk acceptance unexpectedly persisted")
	}
	if node.config.Connections[intent.ID].RiskAccepted {
		t.Fatal("failed persistence changed in-memory risk acceptance")
	}
}

func TestRestoredConnectionReattachesAfterAgentRestart(t *testing.T) {
	backend, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var request [40]byte
				if _, err := io.ReadFull(conn, request[:]); err != nil || !validImport(request, "1-4") {
					return
				}
				_, _ = conn.Write([]byte{0x01, 0x11, 0x00, 0x03, 0x00, 0x00, 0x00, 0x00})
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()

	hostAdapter := &testAdapter{device: platform.Device{ID: "board", BusID: "1-4", VendorID: "303a", ProductID: "1001", Serial: "lab"}}
	host, address := nodeForTest(t, t.Context(), hostAdapter, backend.Addr().String())
	if err := host.setShared(t.Context(), "board", true); err != nil {
		t.Fatal(err)
	}
	invite, err := host.newInvite()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	clientAdapter := &testAdapter{}
	firstContext, stopFirst := context.WithCancel(t.Context())
	first, err := New(firstContext, clientAdapter, dir, "client", "", backend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	peer, err := first.pair(t.Context(), address, invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	created, err := first.connect(t.Context(), peer, "board")
	if err != nil {
		t.Fatal(err)
	}
	requireEventually(t, "first agent attached", func() bool {
		views := first.connectionViews()
		return len(views) == 1 && views[0].State == "attached"
	})
	first.mu.Lock()
	oldSession := first.connections[created.ID]
	first.mu.Unlock()
	stopFirst()
	select {
	case <-oldSession.done:
	case <-time.After(4 * time.Second):
		t.Fatal("first agent did not release its import")
	}

	secondContext, stopSecond := context.WithCancel(t.Context())
	defer stopSecond()
	second, err := New(secondContext, clientAdapter, dir, "client", "", backend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	requireEventually(t, "restored agent reattached", func() bool {
		views := second.connectionViews()
		return len(views) == 1 && views[0].ID == created.ID && views[0].State == "attached"
	})
	if err := second.disconnect(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
}

func TestFollowLeaseKeepsImportThroughTemporaryRenewalFailure(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	oldDevice := platform.Device{ID: "device", Serial: "old"}
	newDevice := platform.Device{ID: "device", Serial: "new"}
	lease := Lease{ID: "test", BusID: "1-4", Online: true, DeviceInfo: &oldDevice}
	renewed := lease
	renewed.DeviceInfo = &newDevice
	imported := platform.Import{Port: 3, RemoteHost: host, RemotePort: port, RemoteBusID: lease.BusID}
	adapter := &testAdapter{imported: &imported}
	node := &Node{adapter: adapter}

	var renewals atomic.Int32
	retried := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch renewals.Add(1) {
		case 1:
			problem(w, failure(http.StatusServiceUnavailable, "temporary renewal failure"))
		case 2:
			close(retried)
			jsonResponse(w, http.StatusOK, renewed)
		default:
			problem(w, failure(http.StatusUnauthorized, "peer revoked"))
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session := &connection{
		Connection: Connection{ID: lease.ID},
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	go func() {
		_, _, _ = node.followLease(ctx, session, server.Client(), Peer{Endpoint: server.Listener.Addr().String()}, lease, listener)
		close(session.done)
	}()
	select {
	case <-retried:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not retry the lease renewal")
	}
	if imports, err := adapter.ListImports(ctx); err != nil || len(imports) != 1 || imports[0].Port != imported.Port {
		t.Fatalf("temporary failure lost the import: %v, %v", imports, err)
	}
	select {
	case <-session.done:
	case <-time.After(3 * time.Second):
		t.Fatal("client kept the lease after receiving an explicit rejection")
	}
	if imports, err := adapter.ListImports(t.Context()); err != nil || len(imports) != 0 {
		t.Fatalf("rejected lease kept an import: %v, %v", imports, err)
	}
	if session.DeviceInfo == nil || session.DeviceInfo.Serial != newDevice.Serial {
		t.Fatalf("renewed device metadata = %+v", session.DeviceInfo)
	}
}

func TestPairingInviteExhaustsAfterThreeAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	host, address := nodeForTest(t, ctx, &testAdapter{}, "127.0.0.1:3240")
	client, _ := nodeForTest(t, ctx, &testAdapter{}, "127.0.0.1:3240")
	invite, err := host.newInvite()
	if err != nil {
		t.Fatal(err)
	}
	if len(invite.Code) != 4 {
		t.Fatalf("invite code = %q, want 4 digits", invite.Code)
	}
	wrong := "0000"
	if wrong == invite.Code {
		wrong = "0001"
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := client.pair(ctx, address, wrong); !isHTTPStatus(err, http.StatusForbidden) {
			t.Fatalf("wrong pairing attempt %d = %v, want 403", attempt, err)
		}
	}
	if _, err := client.pair(ctx, address, invite.Code); !isHTTPStatus(err, http.StatusForbidden) {
		t.Fatalf("pairing after exhausting 3 attempts = %v, want 403", err)
	}
	if _, err := client.pair(ctx, address, "12345678"); !isHTTPStatus(err, http.StatusBadRequest) {
		t.Fatalf("non-4-digit code = %v, want 400", err)
	}
}

func TestAcquireSupersedesZombieTunnelForSameOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	backend, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var req [40]byte
				if _, err := io.ReadFull(conn, req[:]); err != nil {
					return
				}
				_, _ = conn.Write([]byte{0x01, 0x11, 0x00, 0x03, 0x00, 0x00, 0x00, 0x00})
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()

	hostAdapter, err := platformmock.New(ctx, "basic")
	if err != nil {
		t.Fatal(err)
	}
	host, endpoint := nodeForTest(t, ctx, hostAdapter, backend.Addr().String())
	client, _ := nodeForTest(t, ctx, &testAdapter{}, backend.Addr().String())
	const device = "mock-port-1"
	if err := host.setShared(ctx, device, true); err != nil {
		t.Fatal(err)
	}
	invite, err := host.newInvite()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := client.pair(ctx, endpoint, invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	le, err := host.acquireWithPolicy(ctx, client.id, device, "usbip", false)
	if err != nil {
		t.Fatal(err)
	}
	peerClient := client.peerClient(peer.ID)
	defer peerClient.CloseIdleConnections()

	// Open a tunnel that holds stopTunnel open like a stale/half-open NAT session.
	tunnelDone := make(chan struct{})
	go func() {
		defer close(tunnelDone)
		_ = client.followLeaseOpenTunnelForTest(ctx, peerClient, endpoint, le)
	}()
	requireEventually(t, "first tunnel active on host", func() bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		cur := host.leases[device]
		return cur != nil && cur.stopTunnel != nil
	})

	// Re-acquiring the lease from the same owner must supersede the zombie tunnel cleanly.
	le2, err := host.acquireWithPolicy(ctx, client.id, device, "usbip", false)
	if err != nil || le2.ID != le.ID {
		t.Fatalf("supersede acquire = %+v, %v", le2, err)
	}
	select {
	case <-tunnelDone:
	case <-time.After(2 * time.Second):
		t.Fatal("zombie tunnel was not closed on reacquire")
	}
	host.mu.Lock()
	cur := host.leases[device]
	hasStop := cur != nil && cur.stopTunnel != nil
	host.mu.Unlock()
	if hasStop {
		t.Fatal("host still has stale stopTunnel after supersede")
	}
}

func (n *Node) followLeaseOpenTunnelForTest(ctx context.Context, client *http.Client, endpoint string, le Lease) error {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	localCtx, localCancel := context.WithCancel(ctx)
	defer localCancel()
	errCh := make(chan error, 1)
	go func() {
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(localCtx, "tcp", ln.Addr().String())
		if err != nil {
			errCh <- err
			ln.Close()
			return
		}
		defer conn.Close()
		req := packet(le.BusID)
		_, _ = conn.Write(req[:])
		var reply [8]byte
		_, _ = io.ReadFull(conn, reply[:])
		_, _ = io.Copy(io.Discard, conn)
		errCh <- nil
		_ = ln.Close()
		localCancel()
	}()
	n.acceptLocal(localCtx, &connection{Connection: Connection{ID: le.ID}}, ln, Peer{ID: "host", Endpoint: endpoint}, le.ID, client, nil)
	return <-errCh
}

func TestResolvePeerByNamePrefixAndAmbiguity(t *testing.T) {
	idA := strings.Repeat("a", 64)
	idB := "abcdef" + strings.Repeat("b", 58)
	n := &Node{
		config: config{
			Peers: map[string]Peer{
				idA: {ID: idA, Name: "Ubuntu-Lab", Endpoint: "100.64.0.1:47443"},
				idB: {ID: idB, Name: "Win-Client", Endpoint: "100.64.0.2:47443"},
			},
			Shares:      make(map[string][]descriptor),
			Connections: make(map[string]connectionIntent),
		},
		dir: t.TempDir(),
	}
	if p, err := n.findPeer("ubuntu-lab"); err != nil || p.ID != idA {
		t.Fatalf("findPeer by name = %+v, %v", p, err)
	}
	if p, err := n.findPeer("abcdef"); err != nil || p.ID != idB {
		t.Fatalf("findPeer by prefix = %+v, %v", p, err)
	}
	idC := "aaaaaa" + strings.Repeat("c", 58)
	n.config.Peers[idC] = Peer{ID: idC, Name: "Ubuntu-Lab", Endpoint: "100.64.0.3:47443"}
	if _, err := n.findPeer("Ubuntu-Lab"); !errors.Is(err, platform.ErrConflict) {
		t.Fatalf("ambiguous findPeer = %v, want ErrConflict", err)
	}
}
