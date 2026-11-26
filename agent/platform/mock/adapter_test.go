package mock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

func TestNCMScenarioUsesNetworkPolicyAndBinding(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	adapter, err := New(ctx, "cdc-ncm")
	if err != nil {
		t.Fatal(err)
	}
	devices, err := adapter.ListLocalDevices(ctx)
	if err != nil || len(devices) != 1 || devices[0].Risk != "network" || devices[0].SharePolicy != platform.ShareConfirm {
		t.Fatalf("mock NCM device = %+v, %v", devices, err)
	}
	if _, err := adapter.Export(ctx, devices[0].ID); err != nil {
		t.Fatal(err)
	}
	imported, err := adapter.Attach(ctx, adapter.BackendAddress(), devices[0].BusID)
	if err != nil || imported.Readiness != "enumerating" {
		t.Fatalf("mock NCM import = %+v, %v", imported, err)
	}
	adapter.mu.Lock()
	adapter.imports[imported.Port].readyAt = time.Time{}
	adapter.mu.Unlock()
	imports, err := adapter.ListImports(ctx)
	if err != nil || len(imports) != 1 || imports[0].Readiness != "ready" || len(imports[0].Bindings) != 1 || imports[0].Bindings[0].Kind != "network" {
		t.Fatalf("mock NCM binding = %+v, %v", imports, err)
	}
	if err := adapter.Detach(ctx, imported.Port); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateScenarioUsesSeparatePhysicalAndVirtualPorts(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	adapter, err := New(ctx, "duplicate")
	if err != nil {
		t.Fatal(err)
	}
	devices, err := adapter.ListLocalDevices(ctx)
	if err != nil || len(devices) != 2 || devices[0].ID == devices[1].ID || devices[0].PortPath == devices[1].PortPath || devices[0].Serial != devices[1].Serial {
		t.Fatalf("duplicate devices = %+v, %v", devices, err)
	}
	for _, device := range devices {
		if _, err := adapter.Export(ctx, device.ID); err != nil {
			t.Fatal(err)
		}
		imported, err := adapter.Attach(ctx, adapter.BackendAddress(), device.BusID)
		if err != nil || imported.RemoteBusID != device.BusID {
			t.Fatalf("mock attach %s = %+v, %v", device.BusID, imported, err)
		}
	}
	adapter.mu.Lock()
	for _, imported := range adapter.imports {
		imported.readyAt = time.Time{}
	}
	adapter.mu.Unlock()
	imports, err := adapter.ListImports(ctx)
	if err != nil || len(imports) != 2 || imports[0].Port == imports[1].Port || imports[0].SerialPorts[0] == imports[1].SerialPorts[0] {
		t.Fatalf("duplicate imports = %+v, %v", imports, err)
	}
}

func TestBusyAndUnknownScenariosAreTyped(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	adapter, err := New(ctx, "device-busy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Export(ctx, "mock-port-1"); !errors.Is(err, platform.ErrConflict) {
		t.Fatalf("busy export = %v", err)
	}
	if _, err := New(ctx, "not-a-scenario"); !errors.Is(err, platform.ErrInvalid) {
		t.Fatalf("unknown scenario = %v", err)
	}
}
