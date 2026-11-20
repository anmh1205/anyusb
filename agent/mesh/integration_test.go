package mesh

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

type testAdapter struct {
	mu       sync.Mutex
	device   platform.Device
	imported *platform.Import
	socket   net.Conn
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

func nodeForTest(t *testing.T, ctx context.Context, adapter *testAdapter, backend string) (*Node, string) {
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
	wrong := "00000000"
	if wrong == invite.Code {
		wrong = "00000001"
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
	if _, err := second.connect(ctx, competitor, "board"); !isHTTPStatus(err, http.StatusConflict) {
		t.Fatalf("second USB/IP client = %v", err)
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
	if _, err := second.connect(ctx, competitor, "board"); !isHTTPStatus(err, http.StatusConflict) {
		t.Fatalf("USB/IP while ESP busy = %v", err)
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
