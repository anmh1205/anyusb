package linux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
)

const hotplugInterval = time.Second

func readAttribute(devicePath, name string) string {
	value, _ := os.ReadFile(filepath.Join(devicePath, name))
	return strings.TrimSpace(string(value))
}

func (a *Adapter) ListLocalDevices(ctx context.Context) ([]platform.Device, error) {
	directory := filepath.Join(a.sysRoot, "bus", "usb", "devices")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read Linux USB inventory: %w", err)
	}
	devices := make([]platform.Device, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		busID := entry.Name()
		if !busIDPattern.MatchString(busID) {
			continue
		}
		path := filepath.Join(directory, busID)
		physicalPath, err := filepath.EvalSymlinks(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve USB device %s: %w", busID, err)
		}
		if strings.Contains(physicalPath, "/vhci_hcd.") || readAttribute(path, "bDeviceClass") == "09" {
			continue
		}
		deviceNumber := readAttribute(path, "devnum")
		vendor, product := readAttribute(path, "idVendor"), readAttribute(path, "idProduct")
		if deviceNumber == "" || vendor == "" || product == "" {
			continue
		}
		driver, _ := os.Readlink(filepath.Join(path, "driver"))
		portPath := a.portPath(physicalPath, busID)
		hash := sha256.Sum256([]byte("anyusb-device-v1\x00" + a.hostID + "\x00" + portPath))
		device := platform.Device{
			ID: hex.EncodeToString(hash[:16]), BusID: busID, PortPath: portPath,
			VendorID: vendor, ProductID: product, Serial: readAttribute(path, "serial"),
			Manufacturer: readAttribute(path, "manufacturer"), Product: readAttribute(path, "product"),
			Incarnation: busID + "/" + deviceNumber, Exported: filepath.Base(driver) == "usbip-host",
		}
		if readAttribute(path, "devnum") == deviceNumber {
			devices = append(devices, device)
		}
	}
	return devices, nil
}

func (a *Adapter) portPath(physicalPath, busID string) string {
	bus, ports, _ := strings.Cut(busID, "-")
	controller, _, found := strings.Cut(physicalPath, "/usb"+bus+"/")
	if found {
		rootHub := filepath.Join(controller, "usb"+bus)
		if speed := readAttribute(rootHub, "speed"); speed != "" {
			return strings.TrimPrefix(controller, a.sysRoot+"/devices/") + "/usb-" + speed + "/" + ports
		}
	}
	return strings.TrimPrefix(physicalPath, a.sysRoot+"/devices/")
}

func (a *Adapter) findDevice(ctx context.Context, target string) (platform.Device, error) {
	if target == "" {
		return platform.Device{}, fmt.Errorf("%w: provide a device ID or busid", platform.ErrInvalid)
	}
	devices, err := a.ListLocalDevices(ctx)
	if err != nil {
		return platform.Device{}, err
	}
	var match platform.Device
	for _, device := range devices {
		if target != device.ID && target != device.BusID {
			continue
		}
		if match.ID != "" {
			return platform.Device{}, fmt.Errorf("%w: multiple devices match %q; select the current busid", platform.ErrConflict, target)
		}
		match = device
	}
	if match.ID == "" {
		return platform.Device{}, fmt.Errorf("%w: %q", platform.ErrNotFound, target)
	}
	return match, nil
}

func (a *Adapter) Export(ctx context.Context, target string) (platform.Device, error) {
	return a.setExport(ctx, target, true)
}

func (a *Adapter) Unexport(ctx context.Context, target string) (platform.Device, error) {
	return a.setExport(ctx, target, false)
}

func (a *Adapter) setExport(ctx context.Context, target string, exported bool) (platform.Device, error) {
	device, err := a.findDevice(ctx, target)
	if err != nil || device.Exported == exported {
		return device, err
	}
	path := filepath.Join(a.sysRoot, "bus", "usb", "devices", device.BusID)
	verb := "bind"
	if !exported {
		verb = "unbind"
		// SDEV_ST_AVAILABLE is 1. An active or unknown export must be released first.
		if readAttribute(path, "usbip_status") != "1" {
			return platform.Device{}, fmt.Errorf("%w: release the import for %s before unsharing", platform.ErrConflict, device.BusID)
		}
	}
	if _, err := a.run(ctx, "usbip", verb, "--busid="+device.BusID); err != nil {
		return platform.Device{}, err
	}
	current, err := a.findDevice(ctx, device.BusID)
	if err != nil {
		return platform.Device{}, fmt.Errorf("%w: %s completed but inventory could not be confirmed: %v", platform.ErrConflict, verb, err)
	}
	if !sameIncarnation(device, current) || current.Exported != exported {
		return platform.Device{}, fmt.Errorf("%w: %s completed but %s changed; list devices again", platform.ErrConflict, verb, device.BusID)
	}
	return current, nil
}

func (a *Adapter) WatchHotplug(ctx context.Context) (<-chan platform.HotplugEvent, error) {
	devices, err := a.ListLocalDevices(ctx)
	if err != nil {
		return nil, err
	}
	events := make(chan platform.HotplugEvent, 16)
	go func() {
		defer close(events)
		ticker := time.NewTicker(hotplugInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := a.ListLocalDevices(ctx)
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

func inventoryChanges(previous, current []platform.Device) []platform.HotplugEvent {
	old := devicesByBusID(previous)
	now := devicesByBusID(current)
	events := make([]platform.HotplugEvent, 0)
	for _, device := range previous {
		if replacement, ok := now[device.BusID]; !ok || !sameIncarnation(device, replacement) {
			events = append(events, platform.HotplugEvent{Type: "removed", Device: device})
		}
	}
	for _, device := range current {
		before, ok := old[device.BusID]
		var kind string
		switch {
		case !ok || !sameIncarnation(before, device):
			kind = "arrived"
		case !before.Exported && device.Exported:
			kind = "exported"
		case before.Exported && !device.Exported:
			kind = "unexported"
		case before != device:
			kind = "changed"
		}
		if kind != "" {
			events = append(events, platform.HotplugEvent{Type: kind, Device: device})
		}
	}
	return events
}

func sameIncarnation(left, right platform.Device) bool {
	return left.ID == right.ID && left.Incarnation == right.Incarnation &&
		left.VendorID == right.VendorID && left.ProductID == right.ProductID && left.Serial == right.Serial
}

func devicesByBusID(devices []platform.Device) map[string]platform.Device {
	result := make(map[string]platform.Device, len(devices))
	for _, device := range devices {
		result[device.BusID] = device
	}
	return result
}
