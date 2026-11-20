package linux

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

func (a *Adapter) Doctor(ctx context.Context) []platform.Check {
	checks := make([]platform.Check, 0, 5)
	for _, tool := range []struct{ name, versionFlag string }{{"usbip", "version"}, {"usbipd", "--version"}} {
		version, err := a.run(ctx, tool.name, tool.versionFlag)
		check := platform.Check{Name: tool.name, OK: err == nil, Detail: strings.TrimSpace(version)}
		if err != nil {
			check.Detail = err.Error()
		}
		checks = append(checks, check)
	}
	for _, driver := range []struct{ module, bus, dir string }{
		{"usbip-host", "usb", "usbip-host"},
		{"vhci-hcd", "platform", "vhci_hcd"},
	} {
		path := filepath.Join(a.sysRoot, "bus", driver.bus, "drivers", driver.dir)
		info, err := os.Stat(path)
		ready := err == nil && info.IsDir()
		check := platform.Check{Name: driver.module, OK: ready, Detail: "kernel driver available"}
		if !ready {
			check.Detail = "kernel driver unavailable; try sudo modprobe " + driver.module
		}
		checks = append(checks, check)
	}
	probe := platform.Check{Name: "TCP 3240 on loopback"}
	dialer := net.Dialer{Timeout: time.Second}
	for _, host := range []string{"127.0.0.1", "::1"} {
		address := net.JoinHostPort(host, usbipPort)
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			conn.Close()
			probe.OK, probe.Detail = true, address+" accepts connections"
			break
		}
	}
	if !probe.OK {
		probe.Detail = fmt.Sprintf("port %s is unreachable on loopback; check usbipd if this machine exports devices", usbipPort)
	}
	return append(checks, probe)
}
