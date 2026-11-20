package windows

import "testing"

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
