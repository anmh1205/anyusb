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

	"github.com/anmh1205/anyusb/agent/platform"
)

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
		if !platform.BusIDPattern.MatchString(busID) {
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
			Incarnation: busID + "/" + deviceNumber, USBVersion: readAttribute(path, "version"),
			Speed: readAttribute(path, "speed"), DeviceClass: readAttribute(path, "bDeviceClass"),
			DeviceSubClass: readAttribute(path, "bDeviceSubClass"), DeviceProtocol: readAttribute(path, "bDeviceProtocol"),
			Functions: a.usbFunctions(busID), Exported: filepath.Base(driver) == "usbip-host",
		}
		if readAttribute(path, "devnum") == deviceNumber {
			devices = append(devices, platform.EnrichDevice(device))
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
	if !platform.SameIncarnation(device, current) || current.Exported != exported {
		return platform.Device{}, fmt.Errorf("%w: %s completed but %s changed; list devices again", platform.ErrConflict, verb, device.BusID)
	}
	return current, nil
}

func (a *Adapter) WatchHotplug(ctx context.Context) (<-chan platform.HotplugEvent, error) {
	return platform.PollHotplug(ctx, a.ListLocalDevices)
}

func (a *Adapter) LocalSerialPorts(ctx context.Context, target string) ([]string, error) {
	device, err := a.findDevice(ctx, target)
	if err != nil {
		return nil, err
	}
	return a.serialPorts(device.BusID), nil
}

func (a *Adapter) usbFunctions(busID string) []platform.USBFunction {
	directory := filepath.Join(a.sysRoot, "bus", "usb", "devices")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	var functions []platform.USBFunction
	descriptors, _ := os.ReadFile(filepath.Join(directory, busID, "descriptors"))
	controls := cdcControls(descriptors)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), busID+":") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		function := readUSBFunction(path, entry.Name())
		if control, ok := controls[[2]int{function.Configuration, function.Interface}]; ok && function.Class == "0a" {
			function.CDCControl = &control
		}
		functions = append(functions, function)
	}
	return platform.EnrichDevice(platform.Device{Functions: functions}).Functions
}

func readUSBFunction(path, id string) platform.USBFunction {
	driver, _ := os.Readlink(filepath.Join(path, "driver"))
	_, interfacePath, _ := strings.Cut(id, ":")
	configuration, _, _ := strings.Cut(interfacePath, ".")
	function := platform.USBFunction{
		ID: id, Configuration: platform.ParseDecimalAttribute(configuration),
		Interface: platform.ParseHexAttribute(readAttribute(path, "bInterfaceNumber")),
		Alternate: platform.ParseHexAttribute(readAttribute(path, "bAlternateSetting")),
		Class:     readAttribute(path, "bInterfaceClass"), SubClass: readAttribute(path, "bInterfaceSubClass"),
		Protocol: readAttribute(path, "bInterfaceProtocol"), Driver: filepath.Base(driver),
		Description: readAttribute(path, "interface"),
	}
	if function.Driver == "." {
		function.Driver = ""
	}
	return function
}

// cdcControls resolves data interface ownership from IADs and CDC Union
// descriptors. A Union names its members explicitly and takes precedence.
func cdcControls(descriptors []byte) map[[2]int]int {
	controls := make(map[[2]int]int)
	unions := make(map[[2]int]int)
	var configuration, cdcInterface int
	cdcInterface = -1
	for len(descriptors) >= 2 {
		length := int(descriptors[0])
		if length < 2 || length > len(descriptors) {
			break
		}
		current := descriptors[:length]
		descriptors = descriptors[length:]
		switch current[1] {
		case 0x02: // configuration descriptor
			if length >= 9 {
				configuration, cdcInterface = int(current[5]), -1
			}
		case 0x04: // interface descriptor
			cdcInterface = -1
			if length >= 9 && current[5] == 0x02 {
				cdcInterface = int(current[2])
			}
		case 0x0b: // interface association descriptor
			if length >= 8 {
				first, count := int(current[2]), int(current[3])
				for number := first + 1; number < min(first+count, 256); number++ {
					controls[[2]int{configuration, number}] = first
				}
			}
		case 0x24: // class-specific interface descriptor
			if length >= 5 && current[2] == 0x06 && int(current[3]) == cdcInterface {
				for _, member := range current[4:] {
					unions[[2]int{configuration, int(member)}] = cdcInterface
				}
			}
		}
	}
	for member, control := range unions {
		controls[member] = control
	}
	return controls
}
