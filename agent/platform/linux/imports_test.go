package linux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

func TestLinuxPortInventory(t *testing.T) {
	out := "Imported USB devices\n====================\n" +
		"Port 00: <Port in Use> at Full Speed(12Mbps)\n" +
		"       USB JTAG/serial debug unit (303a:1001)\n" +
		"       3-1 -> usbip://127.0.0.1:49152/1-4\n" +
		"           -> remote bus/dev 001/003\n"
	imports, err := parseImports(out)
	if err != nil || len(imports) != 1 || imports[0].LocalBusID != "3-1" || imports[0].RemoteBusID != "1-4" || imports[0].RemotePort != "49152" {
		t.Fatalf("Linux inventory = %+v, %v", imports, err)
	}
}

func TestUSBFunctionsParseCompositeDescriptors(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "interfaces")
	var functions []platform.USBFunction
	for _, fixture := range []struct {
		name, number, class string
	}{
		{"3-1:1.0", "00", "ff"},
		{"3-1:1.1", "01", "03"},
		{"3-1:1.10", "0a", "08"},
	} {
		dir := filepath.Join(base, strings.ReplaceAll(fixture.name, ":", "_"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]string{
			"bConfigurationValue": "1", "bInterfaceNumber": fixture.number,
			"bAlternateSetting": "00", "bInterfaceClass": fixture.class,
			"bInterfaceSubClass": "00", "bInterfaceProtocol": "00",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		functions = append(functions, readUSBFunction(dir, fixture.name))
	}
	functions = platform.EnrichDevice(platform.Device{Functions: functions}).Functions
	if len(functions) != 3 || functions[0].Kind != "vendor" || functions[1].Kind != "hid" || functions[2].Kind != "storage" {
		t.Fatalf("composite functions = %+v", functions)
	}
	if functions[2].Configuration != 1 || functions[2].Interface != 10 {
		t.Fatalf("hex interface descriptor = %+v", functions[2])
	}
}

func TestCDCControlsPrefersExplicitUnionOverIAD(t *testing.T) {
	descriptors := []byte{
		9, 2, 0, 0, 4, 1, 0, 0, 0,
		8, 0x0b, 0, 4, 2, 0x0d, 0, 0,
		9, 4, 2, 0, 1, 2, 2, 0, 0,
		5, 0x24, 6, 2, 3,
		9, 2, 0, 0, 2, 2, 0, 0, 0,
		8, 0x0b, 4, 2, 2, 0x0d, 0, 0,
		9, 4, // truncated descriptor
	}
	controls := cdcControls(descriptors)
	if key := [2]int{1, 3}; controls[key] != 2 {
		t.Fatalf("Union ownership for %v = %v", key, controls)
	}
	if key := [2]int{2, 5}; controls[key] != 4 {
		t.Fatalf("IAD ownership for %v = %v", key, controls)
	}
}

func TestUSBFunctionsAssociateNCMControlAndData(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot represent Linux sysfs interface names containing a colon")
	}
	root := t.TempDir()
	devices := filepath.Join(root, "bus", "usb", "devices")
	physical := filepath.Join(devices, "3-1")
	for _, fixture := range []struct {
		name, number, class, subclass string
	}{
		{"3-1:1.0", "00", "02", "0d"},
		{"3-1:1.1", "01", "0a", "00"},
	} {
		dir := filepath.Join(physical, fixture.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(dir, filepath.Join(devices, fixture.name)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/sys/bus/usb/drivers/cdc_ncm", filepath.Join(dir, "driver")); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]string{
			"bConfigurationValue": "1", "bInterfaceNumber": fixture.number,
			"bAlternateSetting": "00", "bInterfaceClass": fixture.class,
			"bInterfaceSubClass": fixture.subclass, "bInterfaceProtocol": "00",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	adapter := &Adapter{sysRoot: root}
	functions := adapter.usbFunctions("3-1")
	if len(functions) != 2 || functions[0].Kind != "network" || functions[1].Kind != "network" {
		t.Fatalf("NCM functions = %+v", functions)
	}
	netInterface := filepath.Join(root, "class", "net", "usb0")
	if err := os.MkdirAll(netInterface, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(physical, "3-1:1.0"), filepath.Join(netInterface, "device")); err != nil {
		t.Fatal(err)
	}
	imported := platform.FinalizeImport(platform.Import{Functions: functions, Bindings: adapter.localBindings("3-1", functions)})
	if imported.Readiness != "ready" || len(imported.Bindings) != 1 ||
		imported.Bindings[0].ID != "usb0" || imported.Bindings[0].Driver != "cdc_ncm" {
		t.Fatalf("NCM control/data pair and network binding = %+v", imported)
	}
}

func TestLocalBindingWaitsForDeviceNode(t *testing.T) {
	node := filepath.Join(t.TempDir(), "ttyACM0")
	if got := localBindingStatus("serial", node); got != "enumerating" {
		t.Fatalf("missing device node status = %q", got)
	}
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := localBindingStatus("serial", node); got != "ready" {
		t.Fatalf("present device node status = %q", got)
	}
	if got := localBindingStatus("network", "eth-test"); got != "ready" {
		t.Fatalf("network binding status = %q", got)
	}
}

func TestWaitForImportRecoversFromEnumerationError(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	attempts := 0
	port, err := waitForImport(ctx, func(context.Context) (platform.Import, bool, error) {
		attempts++
		if attempts == 1 {
			return platform.Import{}, false, errors.New("usbip port: open vhci_driver")
		}
		return platform.Import{Port: 3}, true, nil
	})
	if err != nil || port.Port != 3 || attempts != 2 {
		t.Fatalf("port = %+v, attempts = %d, error = %v", port, attempts, err)
	}
}
