package platform

import (
	"slices"
	"testing"
)

func TestCDCFunctionClassification(t *testing.T) {
	tests := []struct {
		name        string
		functions   []USBFunction
		wantKinds   []string
		wantProfile string
	}{
		{
			name: "ACM control and data",
			functions: []USBFunction{
				{Configuration: 1, Interface: 0, Class: "02", SubClass: "02"},
				{Configuration: 1, Interface: 1, Class: "0a"},
			},
			wantKinds: []string{"serial", "serial"}, wantProfile: "serial",
		},
		{
			name: "NCM control and data",
			functions: []USBFunction{
				{Configuration: 1, Interface: 0, Class: "02", SubClass: "0d"},
				{Configuration: 1, Interface: 1, Class: "0a", Protocol: "01"},
			},
			wantKinds: []string{"network", "network"}, wantProfile: "network",
		},
		{
			name: "data only is not serial",
			functions: []USBFunction{
				{Configuration: 1, Interface: 1, Class: "0a"},
			},
			wantKinds: []string{functionCDCData}, wantProfile: functionCDCData,
		},
		{
			name: "unknown CDC control is not serial",
			functions: []USBFunction{
				{Configuration: 1, Interface: 0, Class: "02", SubClass: "09"},
			},
			wantKinds: []string{functionCDCControl}, wantProfile: functionCDCControl,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			device := EnrichDevice(Device{Functions: test.functions})
			gotKinds := make([]string, len(device.Functions))
			for index, function := range device.Functions {
				gotKinds[index] = function.Kind
			}
			if !slices.Equal(gotKinds, test.wantKinds) || !slices.Contains(device.Profiles, test.wantProfile) {
				t.Fatalf("classified device = %+v, kinds = %v", device, gotKinds)
			}
		})
	}
}

func TestCDCNetworkSubclasses(t *testing.T) {
	for _, subclass := range []string{"06", "0c", "0d", "0e"} {
		t.Run(subclass, func(t *testing.T) {
			device := EnrichDevice(Device{Functions: []USBFunction{{Class: "02", SubClass: subclass}}})
			if device.Functions[0].Kind != "network" || device.Risk != "network" ||
				device.SharePolicy != ShareConfirm || device.ReconnectPolicy != ReconnectConfirmed {
				t.Fatalf("CDC network device = %+v", device)
			}
		})
	}
}

func TestCompositeCDCNetworkKeepsStoragePolicyPriority(t *testing.T) {
	device := EnrichDevice(Device{Functions: []USBFunction{
		{Configuration: 1, Interface: 0, Class: "02", SubClass: "0d"},
		{Configuration: 1, Interface: 1, Class: "0a"},
		{Configuration: 1, Interface: 2, Class: "08"},
	}})
	if !slices.Contains(device.Profiles, "network") || !slices.Contains(device.Profiles, "storage") ||
		device.Risk != "storage" || device.SharePolicy != ShareConfirm || device.ReconnectPolicy != ReconnectConfirmed {
		t.Fatalf("composite NCM/storage device = %+v", device)
	}
}
