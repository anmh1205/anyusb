// Package mock provides an explicitly development-only USB/IP backend.
// Its scenarios exercise AnyUSB state and UI paths; they are never hardware
// compatibility evidence.
package mock

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

type mockImport struct {
	value   platform.Import
	conn    net.Conn
	readyAt time.Time
}

type Adapter struct {
	ctx      context.Context
	scenario string
	listener net.Listener
	mu       sync.Mutex
	devices  []platform.Device
	imports  map[int]*mockImport
	nextPort int
	lostOnce atomic.Bool
}

var _ platform.Adapter = (*Adapter)(nil)

func New(ctx context.Context, scenario string) (*Adapter, error) {
	scenario = strings.ToLower(strings.TrimSpace(scenario))
	device, err := scenarioDevice(scenario)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	devices := []platform.Device{device}
	if scenario == "duplicate" {
		duplicate := device
		duplicate.ID = "mock-port-2"
		duplicate.BusID = "1-2"
		duplicate.PortPath = "mock/controller-0/port-2"
		duplicate.Incarnation = "mock-duplicate-2"
		devices = append(devices, duplicate)
	}
	adapter := &Adapter{ctx: ctx, scenario: scenario, listener: listener, devices: devices, imports: make(map[int]*mockImport)}
	go adapter.serveBackend()
	go func() {
		<-ctx.Done()
		listener.Close()
		adapter.closeImports()
	}()
	return adapter, nil
}

func (adapter *Adapter) BackendAddress() string { return adapter.listener.Addr().String() }

func scenarioDevice(scenario string) (platform.Device, error) {
	device := platform.Device{
		ID: "mock-port-1", BusID: "1-1", PortPath: "mock/controller-0/port-1",
		VendorID: "1209", ProductID: "a11b", Serial: "MOCK-0001", Manufacturer: "AnyUSB",
		Product: "Development USB device", Incarnation: "mock-device-1", USBVersion: "2.00", Speed: "480",
	}
	switch scenario {
	case "basic", "network-loss", "device-busy", "duplicate":
		device.Functions = []platform.USBFunction{
			{Configuration: 1, Interface: 0, Class: "02", SubClass: "02", Driver: "mock-acm"},
			{Configuration: 1, Interface: 1, Class: "0a", Driver: "mock-acm"},
		}
	case "cdc-ncm":
		device.Product = "Development CDC-NCM adapter"
		device.Functions = []platform.USBFunction{
			{Configuration: 1, Interface: 0, Class: "02", SubClass: "0d", Driver: "mock-ncm"},
			{Configuration: 1, Interface: 1, Class: "0a", Driver: "mock-ncm"},
		}
	case "composite":
		device.Product = "Development CMSIS-DAP composite fixture"
		device.Functions = []platform.USBFunction{
			{Configuration: 1, Interface: 0, Class: "02", SubClass: "02", Driver: "mock-acm"},
			{Configuration: 1, Interface: 1, Class: "0a", Driver: "mock-acm"},
			{Configuration: 1, Interface: 2, Class: "08", Driver: "mock-storage"},
			{Configuration: 1, Interface: 3, Class: "ff", Driver: "mock-winusb"},
		}
	case "driver-missing":
		device.Product = "Development driver-missing fixture"
		device.Functions = []platform.USBFunction{{Configuration: 1, Interface: 0, Class: "ff"}}
	default:
		return platform.Device{}, fmt.Errorf("%w: dev scenario must be basic, cdc-ncm, composite, driver-missing, device-busy, duplicate, or network-loss", platform.ErrInvalid)
	}
	return platform.EnrichDevice(device), nil
}

func (adapter *Adapter) ListLocalDevices(context.Context) ([]platform.Device, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return slices.Clone(adapter.devices), nil
}

func (adapter *Adapter) LocalSerialPorts(_ context.Context, target string) ([]string, error) {
	device, err := adapter.findDevice(target)
	if err != nil {
		return nil, err
	}
	if slices.Contains(device.Profiles, "serial") {
		return []string{"MOCK-COM1"}, nil
	}
	return nil, nil
}

func (adapter *Adapter) Export(_ context.Context, target string) (platform.Device, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.scenario == "device-busy" {
		return platform.Device{}, fmt.Errorf("%w: mock device is busy", platform.ErrConflict)
	}
	for index := range adapter.devices {
		if adapter.devices[index].ID == target || adapter.devices[index].BusID == target {
			adapter.devices[index].Exported = true
			return adapter.devices[index], nil
		}
	}
	return platform.Device{}, platform.ErrNotFound
}

func (adapter *Adapter) Unexport(_ context.Context, target string) (platform.Device, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	for index := range adapter.devices {
		if adapter.devices[index].ID == target || adapter.devices[index].BusID == target {
			adapter.devices[index].Exported = false
			return adapter.devices[index], nil
		}
	}
	return platform.Device{}, platform.ErrNotFound
}

func (adapter *Adapter) ListRemote(context.Context, string) ([]platform.RemoteDevice, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	var devices []platform.RemoteDevice
	for _, device := range adapter.devices {
		if device.Exported {
			devices = append(devices, platform.RemoteDevice{BusID: device.BusID, VendorID: device.VendorID, ProductID: device.ProductID, Name: device.Product})
		}
	}
	return devices, nil
}

func (adapter *Adapter) ListImports(context.Context) ([]platform.Import, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	imports := make([]platform.Import, 0, len(adapter.imports))
	for _, imported := range adapter.imports {
		imports = append(imports, imported.snapshot())
	}
	slices.SortFunc(imports, func(left, right platform.Import) int { return left.Port - right.Port })
	return imports, nil
}

func (adapter *Adapter) Attach(ctx context.Context, host, busID string) (platform.Import, error) {
	peer, service, err := platform.USBIPEndpoint(host)
	if err != nil {
		return platform.Import{}, err
	}
	address := net.JoinHostPort(peer, service)
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return platform.Import{}, err
	}
	request := importRequest(busID)
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(request[:]); err != nil {
		conn.Close()
		return platform.Import{}, err
	}
	var response [8]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil || binary.BigEndian.Uint32(response[4:]) != 0 {
		conn.Close()
		if err == nil {
			err = fmt.Errorf("mock USB/IP backend rejected import")
		}
		return platform.Import{}, err
	}
	var device platform.Device
	if err := json.NewDecoder(io.LimitReader(conn, 8<<10)).Decode(&device); err != nil {
		conn.Close()
		return platform.Import{}, err
	}
	_ = conn.SetDeadline(time.Time{})
	adapter.mu.Lock()
	adapter.nextPort++
	port := adapter.nextPort
	imported := platform.FinalizeImport(platform.Import{
		Port: port, State: "attached", RemoteHost: peer, RemotePort: service, RemoteBusID: busID,
		Name:      device.Product,
		Functions: device.Functions, Bindings: mockBindings(device.Functions, port),
	})
	entry := &mockImport{value: imported, conn: conn, readyAt: time.Now().Add(1400 * time.Millisecond)}
	adapter.imports[port] = entry
	adapter.mu.Unlock()
	go adapter.monitorImport(port, conn)
	return entry.snapshot(), nil
}

func (imported *mockImport) snapshot() platform.Import {
	value := imported.value
	if time.Now().Before(imported.readyAt) {
		value.Readiness = "enumerating"
		value.SerialPorts = nil
		value.Bindings = nil
	}
	return value
}

func mockBindings(functions []platform.USBFunction, port int) []platform.LocalBinding {
	var bindings []platform.LocalBinding
	seen := make(map[string]bool)
	for _, function := range functions {
		if seen[function.Kind] {
			continue
		}
		seen[function.Kind] = true
		id := fmt.Sprintf("mock-%s%d", function.Kind, port)
		if function.Kind == "serial" {
			id = fmt.Sprintf("MOCK-COM%d", port)
		}
		status := "ready"
		if function.Driver == "" {
			status = "driver_missing"
		}
		bindings = append(bindings, platform.LocalBinding{Function: function.ID, Kind: function.Kind, ID: id, Driver: function.Driver, Status: status})
	}
	return bindings
}

func (adapter *Adapter) Detach(_ context.Context, port int) error {
	adapter.mu.Lock()
	imported := adapter.imports[port]
	delete(adapter.imports, port)
	adapter.mu.Unlock()
	if imported != nil {
		return imported.conn.Close()
	}
	return nil
}

func (adapter *Adapter) WatchHotplug(ctx context.Context) (<-chan platform.HotplugEvent, error) {
	return platform.PollHotplug(ctx, adapter.ListLocalDevices)
}

func (adapter *Adapter) Doctor(context.Context) []platform.Check {
	return []platform.Check{{Name: "development mock backend", OK: true, Detail: adapter.scenario + "; not hardware compatibility evidence"}}
}

func (adapter *Adapter) findDevice(target string) (platform.Device, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	for _, device := range adapter.devices {
		if device.ID == target || device.BusID == target {
			return device, nil
		}
	}
	return platform.Device{}, platform.ErrNotFound
}

func (adapter *Adapter) monitorImport(port int, conn net.Conn) {
	_, _ = io.Copy(io.Discard, conn)
	_ = conn.Close()
	adapter.mu.Lock()
	if current := adapter.imports[port]; current != nil && current.conn == conn {
		delete(adapter.imports, port)
	}
	adapter.mu.Unlock()
}

func (adapter *Adapter) closeImports() {
	adapter.mu.Lock()
	imports := adapter.imports
	adapter.imports = make(map[int]*mockImport)
	adapter.mu.Unlock()
	for _, imported := range imports {
		imported.conn.Close()
	}
}

func (adapter *Adapter) serveBackend() {
	for {
		conn, err := adapter.listener.Accept()
		if err != nil {
			return
		}
		go adapter.handleBackend(conn)
	}
}

func (adapter *Adapter) handleBackend(conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(adapter.ctx, func() { _ = conn.Close() })
	defer stop()
	var request [40]byte
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, request[:]); err != nil || !validImportRequest(request) {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	var response [8]byte
	binary.BigEndian.PutUint16(response[0:2], 0x0111)
	binary.BigEndian.PutUint16(response[2:4], 0x0003)
	device, err := adapter.findDevice(string(bytes.TrimRight(request[8:], "\x00")))
	if err != nil || !device.Exported {
		binary.BigEndian.PutUint32(response[4:8], 1)
		_, _ = conn.Write(response[:])
		return
	}
	if _, err := conn.Write(response[:]); err != nil {
		return
	}
	// The mock peer passes its simulated descriptor to the importing mock;
	// production adapters always use the native USB/IP response instead.
	if err := json.NewEncoder(conn).Encode(device); err != nil {
		return
	}
	if adapter.scenario == "network-loss" && adapter.lostOnce.CompareAndSwap(false, true) {
		timer := time.NewTimer(4 * time.Second)
		defer timer.Stop()
		select {
		case <-adapter.ctx.Done():
		case <-timer.C:
		}
		return
	}
	_, _ = io.Copy(io.Discard, conn)
}

func importRequest(busID string) [40]byte {
	var request [40]byte
	binary.BigEndian.PutUint16(request[0:2], 0x0111)
	binary.BigEndian.PutUint16(request[2:4], 0x8003)
	copy(request[8:], busID)
	return request
}

func validImportRequest(request [40]byte) bool {
	return binary.BigEndian.Uint16(request[0:2]) == 0x0111 && binary.BigEndian.Uint16(request[2:4]) == 0x8003 &&
		binary.BigEndian.Uint32(request[4:8]) == 0 && len(bytes.TrimRight(request[8:], "\x00")) > 0
}
