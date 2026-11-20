package platform

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const StandardPort = "3240"

var (
	BusIDPattern = regexp.MustCompile(`^[1-9][0-9]*-[1-9][0-9]*(\.[1-9][0-9]*)*$`)
	labelPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	zonePattern  = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)
	idsPattern   = regexp.MustCompile(`\(([0-9a-fA-F]{4}):([0-9a-fA-F]{4})\)$`)
)

func USBIPEndpoint(endpoint string) (host, port string, err error) {
	host, port = endpoint, StandardPort
	if h, p, splitErr := net.SplitHostPort(endpoint); splitErr == nil {
		host, port = h, p
	}
	number, parseErr := strconv.Atoi(port)
	if parseErr != nil || number < 1 || number > 65535 {
		return "", "", fmt.Errorf("%w: invalid TCP port", ErrInvalid)
	}
	port = strconv.Itoa(number)
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		if zone := addr.Zone(); zone != "" && !zonePattern.MatchString(zone) {
			return "", "", fmt.Errorf("%w: invalid IPv6 scope", ErrInvalid)
		}
		return addr.String(), port, nil
	}
	name := strings.TrimSuffix(host, ".")
	if len(name) > 253 {
		return "", "", fmt.Errorf("%w: hostname is too long", ErrInvalid)
	}
	for _, label := range strings.Split(name, ".") {
		if !labelPattern.MatchString(label) {
			return "", "", fmt.Errorf("%w: provide a hostname or IP address", ErrInvalid)
		}
	}
	return strings.ToLower(host), port, nil
}

func RemoteList(output string) ([]RemoteDevice, error) {
	devices := make([]RemoteDevice, 0)
	for line := range strings.Lines(output) {
		busID, description, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found || !BusIDPattern.MatchString(busID) {
			continue
		}
		device := RemoteDevice{BusID: busID, Name: strings.TrimSpace(description)}
		if ids := idsPattern.FindStringSubmatch(device.Name); ids != nil {
			device.VendorID, device.ProductID = strings.ToLower(ids[1]), strings.ToLower(ids[2])
		}
		devices = append(devices, device)
	}
	clean := strings.TrimSpace(output)
	if clean != "" && len(devices) == 0 && !strings.Contains(strings.ToLower(clean), "no exportable devices") {
		return nil, fmt.Errorf("%w: unrecognized usbip list output", ErrUnavailable)
	}
	return devices, nil
}

func SameIncarnation(left, right Device) bool {
	return left.ID == right.ID && left.Incarnation == right.Incarnation &&
		left.VendorID == right.VendorID && left.ProductID == right.ProductID && left.Serial == right.Serial
}

func PollHotplug(ctx context.Context, list func(context.Context) ([]Device, error)) (<-chan HotplugEvent, error) {
	devices, err := list(ctx)
	if err != nil {
		return nil, err
	}
	events := make(chan HotplugEvent, 16)
	go func() {
		defer close(events)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := list(ctx)
				if err != nil {
					continue
				}
				for _, event := range inventoryChanges(devices, current) {
					select {
					case events <- event:
					case <-ctx.Done():
						return
					}
				}
				devices = current
			}
		}
	}()
	return events, nil
}

func inventoryChanges(previous, current []Device) []HotplugEvent {
	old := devicesByBusID(previous)
	now := devicesByBusID(current)
	var events []HotplugEvent
	for _, device := range previous {
		if replacement, ok := now[device.BusID]; !ok || !SameIncarnation(device, replacement) {
			events = append(events, HotplugEvent{Type: "removed", Device: device})
		}
	}
	for _, device := range current {
		before, ok := old[device.BusID]
		var kind string
		switch {
		case !ok || !SameIncarnation(before, device):
			kind = "arrived"
		case !before.Exported && device.Exported:
			kind = "exported"
		case before.Exported && !device.Exported:
			kind = "unexported"
		case before != device:
			kind = "changed"
		}
		if kind != "" {
			events = append(events, HotplugEvent{Type: kind, Device: device})
		}
	}
	return events
}

func devicesByBusID(devices []Device) map[string]Device {
	result := make(map[string]Device, len(devices))
	for _, device := range devices {
		result[device.BusID] = device
	}
	return result
}
