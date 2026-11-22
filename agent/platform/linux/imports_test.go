package linux

import (
	"context"
	"errors"
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
