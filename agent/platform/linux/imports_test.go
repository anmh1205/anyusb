package linux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	if len(functions) != 3 || functions[0].Kind != "vendor" || functions[1].Kind != "hid" || functions[2].Kind != "storage" {
		t.Fatalf("composite functions = %+v", functions)
	}
	if functions[2].Configuration != 1 || functions[2].Interface != 10 {
		t.Fatalf("hex interface descriptor = %+v", functions[2])
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
