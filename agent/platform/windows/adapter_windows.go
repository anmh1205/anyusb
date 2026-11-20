// Package windows wraps usbipd-win and usbip-win2.
package windows

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/anmh1205/anyusb/agent/platform"
	"golang.org/x/sys/windows/registry"
)

type Adapter struct{ host, client, identity string }

var _ platform.Adapter = (*Adapter)(nil)
var hardwareID = regexp.MustCompile(`(?i)^USB\\VID_([0-9a-f]{4})&PID_([0-9a-f]{4})`)
var comPattern = regexp.MustCompile(`(?i)\bCOM[0-9]+\b`)
var importedID = regexp.MustCompile(`(?i)\(([0-9a-f]{4}):([0-9a-f]{4})\)$`)

func New(host, client string) (*Adapter, error) {
	for _, path := range []string{host, client} {
		if path != "" && !filepath.IsAbs(path) {
			return nil, fmt.Errorf("USB/IP executable overrides must be absolute")
		}
	}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return nil, fmt.Errorf("read Windows machine identity: %w", err)
	}
	defer key.Close()
	id, _, err := key.GetStringValue("MachineGuid")
	if err != nil || id == "" {
		return nil, fmt.Errorf("Windows MachineGuid unavailable")
	}
	return &Adapter{
		host: resolve(host, "usbipd.exe", "usbipd-win"), client: resolve(client, "usbip.exe", "USBip"), identity: id,
	}, nil
}

func resolve(override, file, directory string) string {
	if override != "" {
		return override
	}
	for _, env := range []string{"ProgramW6432", "ProgramFiles"} {
		path := filepath.Join(os.Getenv(env), directory, file)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && filepath.IsAbs(path) {
			return path
		}
	}
	path, _ := exec.LookPath(file)
	return path
}

func run(ctx context.Context, tool, path string, input []byte, args ...string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: install %s or set its executable path", platform.ErrUnavailable, tool)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s: %w", tool, ctx.Err())
	}
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(string(output))
		}
		return "", fmt.Errorf("%s %s: %w: %s", tool, args[0], err, detail)
	}
	return strings.TrimPrefix(string(output), "\ufeff"), nil
}

type deviceState struct {
	BusID, InstanceID, Description, PersistedGuid, ClientIPAddress string
}

type pnpDevice struct {
	InstanceID, Location, Port string
	Virtual                    bool
}

const pnpScript = `
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$ids = @([Console]::In.ReadToEnd() | ConvertFrom-Json)
$com = @(Get-PnpDevice -Class Ports -PresentOnly -ErrorAction SilentlyContinue)
$parents = @{}
function Test-Win2Ancestry([string] $id) {
    for ($i = 0; $id -and $i -lt 12; $i++) {
        if ($id.StartsWith('ROOT\USBIP_WIN2\', [StringComparison]::OrdinalIgnoreCase)) { return $true }
        if (!$parents.ContainsKey($id)) {
            $parents[$id] = (Get-PnpDeviceProperty -InstanceId $id -KeyName DEVPKEY_Device_Parent -ErrorAction SilentlyContinue).Data
        }
        $id = $parents[$id]
    }
    return $false
}
$result = @(
    foreach ($id in $ids) {
        $paths = Get-PnpDeviceProperty -InstanceId $id -KeyName DEVPKEY_Device_LocationPaths -ErrorAction SilentlyContinue
        [pscustomobject]@{InstanceID=$id; Location=[string](@($paths.Data | Sort-Object)[0]); Port=''; Virtual=(Test-Win2Ancestry $id)}
    }
    foreach ($dev in $com) {
        if ($dev.FriendlyName -match '\((COM\d+)\)') {
            $port = $Matches[1]
            $paths = Get-PnpDeviceProperty -InstanceId $dev.InstanceId -KeyName DEVPKEY_Device_LocationPaths -ErrorAction SilentlyContinue
            [pscustomobject]@{InstanceID=$dev.InstanceId; Location=[string](@($paths.Data | Sort-Object)[0]); Port=$port; Virtual=(Test-Win2Ancestry $dev.InstanceId)}
        }
    }
)
ConvertTo-Json -InputObject $result -Compress`

func pnp(ctx context.Context, ids []string) ([]pnpDevice, error) {
	data, _ := json.Marshal(ids)
	path := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	out, err := run(ctx, "PowerShell PnP", path, data, "-NoProfile", "-NonInteractive", "-Command", pnpScript)
	if err != nil {
		return nil, err
	}
	var devices []pnpDevice
	err = json.Unmarshal([]byte(out), &devices)
	return devices, err
}

func (a *Adapter) readState(ctx context.Context) ([]deviceState, error) {
	out, err := run(ctx, "usbipd-win", a.host, nil, "state")
	if err != nil {
		return nil, err
	}
	var state struct{ Devices []deviceState }
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return nil, fmt.Errorf("decode usbipd state: %w", err)
	}
	return state.Devices, nil
}

func (a *Adapter) inventory(ctx context.Context) ([]platform.Device, map[string]deviceState, []pnpDevice, error) {
	state, err := a.readState(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	ids := make([]string, 0)
	for _, item := range state {
		if item.BusID != "" && hardwareID.MatchString(item.InstanceID) {
			ids = append(ids, item.InstanceID)
		}
	}
	ports, err := pnp(ctx, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	locations := make(map[string]string)
	virtual := make(map[string]bool)
	for _, item := range ports {
		virtual[strings.ToUpper(item.InstanceID)] = item.Virtual
		if item.Location != "" {
			locations[strings.ToUpper(item.InstanceID)] = strings.ToUpper(item.Location)
		}
	}
	devices := make([]platform.Device, 0)
	byBus := make(map[string]deviceState)
	for _, item := range state {
		vidpid := hardwareID.FindStringSubmatch(item.InstanceID)
		if item.BusID == "" || len(vidpid) != 3 {
			continue
		}
		location := locations[strings.ToUpper(item.InstanceID)]
		if virtual[strings.ToUpper(item.InstanceID)] || strings.Contains(location, "USBIP_WIN2") || strings.Contains(location, "USBIP2") {
			continue
		}
		if location == "" {
			location = "instance:" + strings.ToUpper(item.InstanceID)
		}
		parts := strings.Split(item.InstanceID, `\`)
		serial := ""
		if len(parts) == 3 && !strings.Contains(parts[2], "&") {
			serial = parts[2]
		}
		key := sha256.Sum256([]byte("anyusb-device-v1\x00" + a.identity + "\x00" + location))
		devices = append(devices, platform.Device{
			ID: fmt.Sprintf("%x", key[:16]), BusID: item.BusID, PortPath: location,
			Incarnation: item.InstanceID, VendorID: strings.ToLower(vidpid[1]), ProductID: strings.ToLower(vidpid[2]),
			Serial: serial, Product: item.Description, Exported: item.PersistedGuid != "",
		})
		byBus[item.BusID] = item
	}
	return devices, byBus, ports, nil
}

func (a *Adapter) ListLocalDevices(ctx context.Context) ([]platform.Device, error) {
	devices, _, _, err := a.inventory(ctx)
	return devices, err
}

func selectDevice(devices []platform.Device, target string) (platform.Device, error) {
	var result platform.Device
	for _, d := range devices {
		if d.ID == target || d.BusID == target {
			if result.ID != "" {
				return platform.Device{}, fmt.Errorf("%w: multiple devices match %q", platform.ErrConflict, target)
			}
			result = d
		}
	}
	if result.ID == "" {
		return result, fmt.Errorf("%w: %q", platform.ErrNotFound, target)
	}
	return result, nil
}

func (a *Adapter) LocalSerialPorts(ctx context.Context, target string) ([]string, error) {
	devices, _, pnpDevices, err := a.inventory(ctx)
	if err != nil {
		return nil, err
	}
	d, err := selectDevice(devices, target)
	if err != nil {
		return nil, err
	}
	var ports []string
	for _, item := range pnpDevices {
		loc := strings.ToUpper(item.Location)
		if item.Port != "" && (strings.EqualFold(item.InstanceID, d.Incarnation) || loc == d.PortPath || strings.HasPrefix(loc, d.PortPath+"#USBMI(")) {
			ports = append(ports, item.Port)
		}
	}
	if len(ports) == 0 {
		ports = comPattern.FindAllString(d.Product, -1)
	}
	return ports, nil
}

func (a *Adapter) Export(ctx context.Context, target string) (platform.Device, error) {
	return a.setExport(ctx, target, true)
}

func (a *Adapter) Unexport(ctx context.Context, target string) (platform.Device, error) {
	return a.setExport(ctx, target, false)
}

func (a *Adapter) setExport(ctx context.Context, target string, shared bool) (platform.Device, error) {
	devices, state, _, err := a.inventory(ctx)
	if err != nil {
		return platform.Device{}, err
	}
	d, err := selectDevice(devices, target)
	if err != nil || d.Exported == shared {
		return d, err
	}
	verb := "bind"
	if !shared {
		if state[d.BusID].ClientIPAddress != "" {
			return d, fmt.Errorf("%w: detach the current import first", platform.ErrConflict)
		}
		verb = "unbind"
	}
	if _, err := run(ctx, "usbipd-win", a.host, nil, verb, "--busid", d.BusID); err != nil {
		return platform.Device{}, err
	}
	current, err := a.readState(ctx)
	if err != nil {
		return d, fmt.Errorf("%w: %s returned success but Windows export state could not be read: %v", platform.ErrConflict, verb, err)
	}
	for _, item := range current {
		if item.BusID == d.BusID && strings.EqualFold(item.InstanceID, d.Incarnation) && (item.PersistedGuid != "") == shared {
			d.Exported = shared
			return d, nil
		}
	}
	return d, fmt.Errorf("%w: device or bind state changed during %s", platform.ErrConflict, verb)
}

func (a *Adapter) WatchHotplug(ctx context.Context) (<-chan platform.HotplugEvent, error) {
	return platform.PollHotplug(ctx, a.ListLocalDevices)
}

func (a *Adapter) ListRemote(ctx context.Context, endpoint string) ([]platform.RemoteDevice, error) {
	host, port, err := platform.USBIPEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, "usbip-win2", a.client, nil, "--tcp-port", port, "list", "--remote", host)
	if err != nil {
		return nil, err
	}
	return platform.RemoteList(out)
}

func (a *Adapter) Attach(ctx context.Context, endpoint, busID string) (platform.Import, error) {
	host, service, err := platform.USBIPEndpoint(endpoint)
	if err != nil || !platform.BusIDPattern.MatchString(busID) {
		return platform.Import{}, fmt.Errorf("%w: invalid USB/IP endpoint or busid", platform.ErrInvalid)
	}
	imports, err := a.ListImports(ctx)
	if err != nil {
		return platform.Import{}, err
	}
	for _, device := range imports {
		if device.RemoteHost == host && device.RemotePort == service && device.RemoteBusID == busID {
			return device, nil
		}
	}
	out, err := run(ctx, "usbip-win2", a.client, nil, "--tcp-port", service, "attach", "--remote", host, "--bus-id", busID, "--once", "--terse")
	if err != nil {
		return platform.Import{}, err
	}
	port, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || port <= 0 {
		return platform.Import{}, fmt.Errorf("%w: attach did not report a valid virtual port; list imports before retrying", platform.ErrConflict)
	}
	return platform.Import{Port: port, State: "attached", RemoteHost: host, RemotePort: service, RemoteBusID: busID}, nil
}

func (a *Adapter) ListImports(ctx context.Context) ([]platform.Import, error) {
	out, err := run(ctx, "usbip-win2", a.client, nil, "port")
	if err != nil {
		return nil, err
	}
	imports, err := parseImports(out)
	if err != nil || len(imports) == 0 {
		return imports, err
	}
	if ports, pnpErr := pnp(ctx, []string{}); pnpErr == nil {
		mapImportCOM(imports, ports)
	}
	return imports, nil
}

func mapImportCOM(imports []platform.Import, devices []pnpDevice) {
	matches := make(map[string]int)
	for _, entry := range imports {
		if ids := importedID.FindStringSubmatch(entry.Name); ids != nil {
			matches[strings.ToLower(ids[1]+":"+ids[2])]++
		}
	}
	for i := range imports {
		ids := importedID.FindStringSubmatch(imports[i].Name)
		if ids == nil {
			continue
		}
		key := strings.ToLower(ids[1] + ":" + ids[2])
		if matches[key] != 1 {
			continue
		} // Ambiguous duplicates are left for the operator to resolve.
		for _, port := range devices {
			vidpid := hardwareID.FindStringSubmatch(port.InstanceID)
			if port.Virtual && port.Port != "" && len(vidpid) == 3 && strings.EqualFold(vidpid[1]+":"+vidpid[2], key) {
				imports[i].SerialPorts = append(imports[i].SerialPorts, port.Port)
			}
		}
	}
}

func parseImports(output string) ([]platform.Import, error) {
	if clean := strings.TrimSpace(output); clean != "" && !strings.HasPrefix(clean, "Imported USB devices") {
		return nil, fmt.Errorf("%w: unexpected usbip-win2 port output", platform.ErrUnavailable)
	}
	imports := make([]platform.Import, 0)
	var item *platform.Import
	for line := range strings.Lines(output) {
		if header, ok := strings.CutPrefix(line, "Port "); ok {
			number, state, _ := strings.Cut(header, ":")
			port, err := strconv.Atoi(number)
			if err != nil || port < 1 {
				return nil, fmt.Errorf("%w: invalid Win2 virtual port %q", platform.ErrUnavailable, number)
			}
			imports = append(imports, platform.Import{Port: port, State: strings.TrimSpace(state)})
			item = &imports[len(imports)-1]
			continue
		}
		if item == nil {
			continue
		}
		line = strings.TrimSpace(line)
		if loc, ok := strings.CutPrefix(line, "-> usbip://"); ok {
			addr, bus, _ := strings.Cut(loc, "/")
			if colon := strings.LastIndexByte(addr, ':'); colon > 0 {
				item.RemoteHost, item.RemotePort, item.RemoteBusID = strings.Trim(addr[:colon], "[]"), addr[colon+1:], bus
			}
		} else if item.Name == "" && !strings.HasPrefix(line, "->") {
			item.Name = line
		}
	}
	return imports, nil
}

func (a *Adapter) Detach(ctx context.Context, port int) error {
	if port < 1 || port > 255 {
		return fmt.Errorf("%w: Win2 virtual port must be 1–255", platform.ErrInvalid)
	}
	devices, err := a.ListImports(ctx)
	if err != nil {
		return err
	}
	for _, device := range devices {
		if device.Port == port {
			_, err = run(ctx, "usbip-win2", a.client, nil, "detach", "--port", strconv.Itoa(port))
			return err
		}
	}
	return nil
}

func (a *Adapter) Doctor(ctx context.Context) []platform.Check {
	checks := make([]platform.Check, 0, 6)
	for _, tools := range []struct{ name, path string }{{"usbipd-win", a.host}, {"usbip-win2", a.client}} {
		version, err := run(ctx, tools.name, tools.path, nil, "--version")
		checks = append(checks, check(tools.name, version, err))
	}
	sc := filepath.Join(os.Getenv("SystemRoot"), "System32", "sc.exe")
	for _, name := range []string{"usbipd", "VBoxUSBMon", "usbip2_ude", "usbip2_filter"} {
		state, err := run(ctx, "sc.exe", sc, nil, "query", name)
		status := "registered in Service Control Manager"
		if strings.Contains(state, "RUNNING") {
			status = "running"
		}
		checks = append(checks, check(name, status, err))
	}
	socket, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", "127.0.0.1:3240")
	if socket != nil {
		socket.Close()
	}
	checks = append(checks, check("TCP 3240 on loopback", "stock USB/IP listener accepts TCP", err))
	if err == nil {
		path := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		output, err := run(ctx, "PowerShell firewall", path, nil, "-NoProfile", "-NonInteractive", "-Command",
			`$rule = @(Get-NetFirewallRule -DisplayName usbipd -ErrorAction SilentlyContinue | Where-Object Enabled -eq True); if ($rule.Count) { 'enabled' } else { 'disabled' }`)
		scoped := err == nil && strings.TrimSpace(output) == "disabled"
		detail := "default usbipd inbound rule is disabled; verify other rules and peer reachability"
		if !scoped {
			detail = "default usbipd inbound rule is enabled or could not be checked; restrict TCP 3240 for managed leases"
		}
		checks = append(checks, platform.Check{Name: "TCP 3240 peer isolation", OK: scoped, Detail: detail})
	}
	return checks
}

func check(name, detail string, err error) platform.Check {
	if err != nil {
		detail = err.Error()
	}
	return platform.Check{Name: name, OK: err == nil, Detail: strings.TrimSpace(detail)}
}
