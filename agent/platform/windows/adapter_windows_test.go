package windows

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/anmh1205/anyusb/agent/platform"
)

func TestWin2PortInventory(t *testing.T) {
	out := "Imported USB devices\r\n====================\r\n" +
		"Port 03: device in use at Full Speed\r\n" +
		"         Espressif : USB JTAG/serial debug unit (303a:1001)\r\n" +
		"           -> usbip://127.0.0.1:53321/1-4.2\r\n" +
		"           -> remote bus/dev: 001/003\r\n"
	imports, err := parseImports(out)
	if err != nil || len(imports) != 1 || imports[0].Port != 3 || imports[0].RemoteBusID != "1-4.2" || imports[0].RemotePort != "53321" {
		t.Fatalf("Win2 inventory = %+v, %v", imports, err)
	}
	mapImportCOM(imports, []pnpDevice{
		{Port: "COM8", InstanceID: `USB\VID_303A&PID_1001&MI_00\1`, Virtual: true},
		{Port: "COM9", InstanceID: `USB\VID_303A&PID_1001&MI_00\2`},
	})
	if len(imports[0].SerialPorts) != 1 || imports[0].SerialPorts[0] != "COM8" {
		t.Fatalf("serial mapping = %v", imports[0].SerialPorts)
	}
	if empty, err := parseImports(""); err != nil || len(empty) > 0 {
		t.Fatalf("empty imports = %v, %v", empty, err)
	}
}

func TestWin2COMMappingUsesVirtualHubPortForIdenticalDevices(t *testing.T) {
	imports := []platform.Import{
		{Port: 3, Name: "Espressif : USB JTAG/serial debug unit (303a:1001)"},
		{Port: 4, Name: "Espressif : USB JTAG/serial debug unit (303a:1001)"},
	}
	mapImportCOM(imports, []pnpDevice{
		{Port: "COM11", InstanceID: `USB\VID_303A&PID_1001&MI_00\A`, Virtual: true, VirtualPort: 4},
		{Port: "COM8", InstanceID: `USB\VID_303A&PID_1001&MI_00\B`, Virtual: true, VirtualPort: 3},
	})
	if len(imports[0].SerialPorts) != 1 || imports[0].SerialPorts[0] != "COM8" {
		t.Fatalf("port 3 serial mapping = %v", imports[0].SerialPorts)
	}
	if len(imports[1].SerialPorts) != 1 || imports[1].SerialPorts[0] != "COM11" {
		t.Fatalf("port 4 serial mapping = %v", imports[1].SerialPorts)
	}
}

func TestPowerShellPnPInstanceIDs(t *testing.T) {
	const fixture = `
function Get-PnpDevice { param($Class, [switch]$PresentOnly) }
function Get-PnpDeviceProperty {
    [CmdletBinding()]
    param([ValidateNotNullOrEmpty()][string]$InstanceId, [string]$KeyName)
    [pscustomobject]@{Data=''}
}
`
	powerShell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	for _, ids := range [][]string{nil, {}, {"USB\\VID_1209&PID_A11B\\ONE"}, {"a", "b"}} {
		payload, err := json.Marshal(ids)
		if err != nil {
			t.Fatal(err)
		}
		output, err := run(t.Context(), "PowerShell PnP", powerShell, payload, "-NoProfile", "-NonInteractive", "-Command", fixture+pnpScript)
		if err != nil {
			t.Fatalf("PnP %v: %v", ids, err)
		}
		var devices []pnpDevice
		if err := json.Unmarshal([]byte(output), &devices); err != nil {
			t.Fatalf("PnP %v: %v (%s)", ids, err, output)
		}
		if len(devices) != len(ids) {
			t.Fatalf("PnP %v returned %+v", ids, devices)
		}
		for i, id := range ids {
			if devices[i].InstanceID != id {
				t.Fatalf("PnP InstanceID = %q, want %q", devices[i].InstanceID, id)
			}
		}
	}
}

func TestPowerShellFindsWin2VirtualHubPort(t *testing.T) {
	const fixture = `
function Get-PnpDevice {
    param($Class, [switch]$PresentOnly)
    [pscustomobject]@{FriendlyName='USB Serial Device (COM8)'; InstanceId='USB\VID_303A&PID_1001&MI_00\A'}
}
function Get-PnpDeviceProperty {
    [CmdletBinding()]
    param([ValidateNotNullOrEmpty()][string]$InstanceId, [string]$KeyName)
    $data = switch ($KeyName) {
        'DEVPKEY_Device_Parent' {
            switch ($InstanceId) {
                'USB\VID_303A&PID_1001&MI_00\A' { 'USB\VID_303A&PID_1001\A' }
                'USB\VID_303A&PID_1001\A' { 'USB\ROOT_HUB30\VIRTUAL' }
                'USB\ROOT_HUB30\VIRTUAL' { 'ROOT\USBIP_WIN2\UDE\0000' }
                default { '' }
            }
        }
        'DEVPKEY_Device_Address' { if ($InstanceId -eq 'USB\VID_303A&PID_1001\A') { 3 } else { 0 } }
        'DEVPKEY_Device_LocationPaths' { 'USBROOT(0)#USB(3)' }
        default { '' }
    }
    [pscustomobject]@{Data=$data}
}
`
	powerShell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	output, err := run(t.Context(), "PowerShell PnP", powerShell, []byte("[]"), "-NoProfile", "-NonInteractive", "-Command", fixture+pnpScript)
	if err != nil {
		t.Fatal(err)
	}
	var devices []pnpDevice
	if err := json.Unmarshal([]byte(output), &devices); err != nil {
		t.Fatalf("decode PnP output: %v (%s)", err, output)
	}
	if len(devices) != 1 || !devices[0].Virtual || devices[0].VirtualPort != 3 || devices[0].Port != "COM8" {
		t.Fatalf("virtual COM topology = %+v", devices)
	}
}
