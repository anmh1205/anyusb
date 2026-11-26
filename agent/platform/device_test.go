package platform

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnrichDeviceProfilesAndPolicies(t *testing.T) {
	tests := []struct {
		name      string
		device    Device
		profile   string
		risk      string
		share     string
		reconnect string
	}{
		{"serial", Device{Functions: []USBFunction{{Configuration: 1, Interface: 0, Class: "02", SubClass: "02"}, {Configuration: 1, Interface: 1, Class: "0a"}}}, "serial", "", ShareAllow, ReconnectAutomatic},
		{"network NCM", Device{Functions: []USBFunction{{Configuration: 1, Interface: 0, Class: "02", SubClass: "0d"}, {Configuration: 1, Interface: 1, Class: "0a"}}}, "network", "network", ShareConfirm, ReconnectConfirmed},
		{"storage", Device{Functions: []USBFunction{{Class: "08"}}}, "storage", "storage", ShareConfirm, ReconnectConfirmed},
		{"hid", Device{Functions: []USBFunction{{Class: "03"}}}, "hid", "input", ShareConfirm, ReconnectConfirmed},
		{"hub", Device{DeviceClass: "09"}, "hub", "topology", ShareBlock, ReconnectConfirmed},
		{"named hub", Device{Product: "Generic USB Hub"}, "hub", "topology", ShareBlock, ReconnectConfirmed},
		{"hub with storage", Device{Functions: []USBFunction{{Class: "09"}, {Class: "08"}}}, "hub", "topology", ShareBlock, ReconnectConfirmed},
		{"hub with network", Device{Functions: []USBFunction{{Class: "09"}, {Class: "e0"}}}, "hub", "topology", ShareBlock, ReconnectConfirmed},
		{"stlink", Device{VendorID: "0483", ProductID: "374b", Product: "ST-LINK/V2.1"}, "stlink", "", ShareAllow, ReconnectAutomatic},
		{"jlink", Device{VendorID: "1366", ProductID: "0105", Product: "J-Link"}, "jlink", "", ShareAllow, ReconnectAutomatic},
		{"unknown", Device{DeviceClass: "00"}, "unknown", "", ShareAllow, ReconnectAutomatic},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := EnrichDevice(test.device)
			if !contains(got.Profiles, test.profile) || got.Risk != test.risk || got.SharePolicy != test.share || got.ReconnectPolicy != test.reconnect {
				t.Fatalf("enriched device = %+v", got)
			}
		})
	}
}

func TestFinalizeImportBindings(t *testing.T) {
	got := FinalizeImport(Import{State: "attached", Bindings: []LocalBinding{
		{Kind: "serial", ID: "COM8", Status: "ready", Driver: "usbser"},
		{Kind: "serial", ID: "COM8", Status: "ready", Driver: "usbser"},
		{Kind: "serial", ID: "1-2:1.1", Status: "bound", Driver: "cdc_acm"},
		{Kind: "debug", ID: "USB\\VID_0483&PID_374B&MI_00", Status: "driver_missing"},
	}})
	if got.Readiness != "degraded" || len(got.SerialPorts) != 1 || got.SerialPorts[0] != "COM8" {
		t.Fatalf("final import = %+v", got)
	}
}

func TestFinalizeImportWaitsForExpectedOSBinding(t *testing.T) {
	got := FinalizeImport(Import{State: "attached", Bindings: []LocalBinding{
		{Kind: "serial", ID: "/dev/ttyACM0", Status: "ready", Driver: "cdc_acm"},
		{Kind: "storage", ID: "2-1:1.3", Status: "enumerating", Driver: "usb-storage"},
	}})
	if got.Readiness != "enumerating" {
		t.Fatalf("enumerating import = %+v", got)
	}
}

func TestGenericUSBEnumerationDoesNotStayPending(t *testing.T) {
	got := FinalizeImport(Import{State: "attached", Bindings: []LocalBinding{
		{Kind: "usb", ID: "/dev/bus/usb/001/002", Status: "enumerated"},
	}})
	if got.Readiness != "unknown" {
		t.Fatalf("generic import = %+v", got)
	}
}

func TestUSBAttributeNumbers(t *testing.T) {
	if got := ParseHexAttribute("0a"); got != 10 {
		t.Fatalf("hex interface number = %d", got)
	}
	if got := ParseDecimalAttribute("12"); got != 12 {
		t.Fatalf("decimal configuration = %d", got)
	}
}

func TestImportJSONKeepsSerialCompatibilityAndAddsBindings(t *testing.T) {
	data, err := json.Marshal(FinalizeImport(Import{State: "attached", Bindings: []LocalBinding{
		{Function: "1:0.0", Kind: "serial", ID: "/dev/ttyACM0", Driver: "cdc_acm", Status: "ready"},
		{Function: "1:1.0", Kind: "debug", ID: "/dev/bus/usb/001/002", Driver: "usbfs", Status: "ready"},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(data)
	for _, field := range []string{`"local_bindings"`, `"serial_ports":["/dev/ttyACM0"]`, `"readiness":"ready"`} {
		if !strings.Contains(jsonText, field) {
			t.Fatalf("compatibility JSON %s is missing %s", jsonText, field)
		}
	}
}

func TestImportBindingCheckSeparatesBackendFromReadiness(t *testing.T) {
	check := ImportBindingCheck([]Import{{Readiness: "ready"}, {Readiness: "driver_missing"}}, nil)
	if check.OK || !strings.Contains(check.Detail, "1 need inspection") {
		t.Fatalf("binding check = %+v", check)
	}
	empty := ImportBindingCheck(nil, nil)
	if !empty.OK {
		t.Fatalf("empty binding check = %+v", empty)
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
