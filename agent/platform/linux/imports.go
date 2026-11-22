package linux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

const usbipPort = platform.StandardPort

func (a *Adapter) ListRemote(ctx context.Context, endpoint string) ([]platform.RemoteDevice, error) {
	peer, port, err := platform.USBIPEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	output, err := a.run(ctx, "usbip", "--tcp-port="+port, "list", "--remote="+peer)
	if err != nil {
		return nil, err
	}
	return platform.RemoteList(output)
}

func (a *Adapter) ListImports(ctx context.Context) ([]platform.Import, error) {
	output, err := a.run(ctx, "usbip", "port")
	if err != nil {
		return nil, err
	}
	imports, err := parseImports(output)
	if err != nil {
		return nil, err
	}
	for i := range imports {
		imports[i].SerialPorts = a.serialPorts(imports[i].LocalBusID)
	}
	return imports, nil
}

func parseImports(output string) ([]platform.Import, error) {
	if !strings.HasPrefix(strings.TrimSpace(output), "Imported USB devices") {
		return nil, fmt.Errorf("%w: unrecognized usbip port output", platform.ErrUnavailable)
	}
	imports := make([]platform.Import, 0)
	var current *platform.Import
	for line := range strings.Lines(output) {
		if header, ok := strings.CutPrefix(line, "Port "); ok {
			number, state, found := strings.Cut(header, ":")
			port, err := strconv.Atoi(number)
			if !found || err != nil || port < 0 {
				return nil, fmt.Errorf("%w: unrecognized USB/IP port header %q", platform.ErrUnavailable, line)
			}
			imports = append(imports, platform.Import{Port: port, State: strings.TrimSpace(state)})
			current = &imports[len(imports)-1]
			continue
		}
		line = strings.TrimSpace(line)
		if current == nil || line == "" {
			continue
		}
		if busID, target, found := strings.Cut(line, " -> "); found {
			if platform.BusIDPattern.MatchString(busID) {
				current.LocalBusID = busID
			}
			if endpoint, ok := strings.CutPrefix(target, "usbip://"); ok {
				address, remoteBusID, hasPath := strings.Cut(endpoint, "/")
				if separator := strings.LastIndexByte(address, ':'); separator > 0 && hasPath {
					current.RemoteHost = strings.Trim(address[:separator], "[]")
					current.RemotePort = address[separator+1:]
					current.RemoteBusID = remoteBusID
				}
			}
			continue
		}
		if current.Name == "" && !strings.HasPrefix(line, "->") {
			current.Name = line
		}
	}
	return imports, nil
}

func (a *Adapter) serialPorts(busID string) []string {
	if busID == "" {
		return nil
	}
	usbPath, err := filepath.EvalSymlinks(filepath.Join(a.sysRoot, "bus", "usb", "devices", busID))
	if err != nil {
		return nil
	}
	ttyDirectory := filepath.Join(a.sysRoot, "class", "tty")
	entries, err := os.ReadDir(ttyDirectory)
	if err != nil {
		return nil
	}
	var ports []string
	for _, entry := range entries {
		devicePath, err := filepath.EvalSymlinks(filepath.Join(ttyDirectory, entry.Name(), "device"))
		if err == nil && strings.HasPrefix(devicePath, usbPath+"/") {
			node := filepath.Join("/dev", entry.Name())
			if info, err := os.Stat(node); err == nil && info.Mode()&os.ModeCharDevice != 0 {
				ports = append(ports, node)
			}
		}
	}
	return ports
}

func (a *Adapter) Attach(ctx context.Context, host, busID string) (platform.Import, error) {
	peer, port, err := platform.USBIPEndpoint(host)
	if err != nil {
		return platform.Import{}, err
	}
	if !platform.BusIDPattern.MatchString(busID) {
		return platform.Import{}, fmt.Errorf("%w: invalid remote USB busid %q", platform.ErrInvalid, busID)
	}
	lookup := func(ctx context.Context) (platform.Import, bool, error) {
		imports, err := a.ListImports(ctx)
		for _, imported := range imports {
			if strings.EqualFold(imported.RemoteHost, peer) && imported.RemotePort == port && imported.RemoteBusID == busID {
				return imported, true, nil
			}
		}
		return platform.Import{}, false, err
	}
	if imported, found, err := lookup(ctx); found || err != nil {
		return imported, err
	}
	if _, err := a.run(ctx, "usbip", "--tcp-port="+port, "attach", "--remote="+peer, "--busid="+busID); err != nil {
		return platform.Import{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return waitForImport(ctx, lookup)
}

func waitForImport(ctx context.Context, lookup func(context.Context) (platform.Import, bool, error)) (platform.Import, error) {
	// usbip port can fail while the kernel is still assigning a local USB address.
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	var probeErr error
	for {
		if imported, found, err := lookup(ctx); found {
			return imported, nil
		} else if err != nil {
			probeErr = err
		}
		select {
		case <-ctx.Done():
			if probeErr != nil {
				return platform.Import{}, fmt.Errorf("%w: attach completed but its port could not be confirmed; run anyusb list --imports: %v", platform.ErrConflict, probeErr)
			}
			return platform.Import{}, fmt.Errorf("%w: attach completed without a confirmed port; run anyusb list --imports", platform.ErrConflict)
		case <-ticker.C:
		}
	}
}

func (a *Adapter) Detach(ctx context.Context, port int) error {
	if port < 0 || port > 255 {
		return fmt.Errorf("%w: USB/IP port must be in the range 0–255", platform.ErrInvalid)
	}
	imports, err := a.ListImports(ctx)
	if err != nil {
		return err
	}
	for _, imported := range imports {
		if imported.Port == port {
			_, err := a.run(ctx, "usbip", "detach", "--port="+strconv.Itoa(port))
			return err
		}
	}
	return nil
}
