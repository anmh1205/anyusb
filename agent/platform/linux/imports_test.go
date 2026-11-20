package linux

import "testing"

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
