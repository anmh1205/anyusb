package platform

import (
	"fmt"
	"slices"
	"strings"
)

const (
	functionCDCControl = "cdc-control"
	functionCDCData    = "cdc-data"
)

var cdcNetworkSubclasses = map[string]bool{
	"06": true, // Ethernet Control Model (ECM)
	"0c": true, // Ethernet Emulation Model (EEM)
	"0d": true, // Network Control Model (NCM)
	"0e": true, // Mobile Broadband Interface Model (MBIM)
}

func normalizeClass(value string) string {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "0x")
	if len(value) == 1 {
		value = "0" + value
	}
	return value
}

// classifyDescriptor classifies one descriptor without guessing how a CDC data
// interface is grouped. classifyFunctions performs that association once it can
// inspect the complete set of interfaces for the physical device.
func classifyDescriptor(class, subclass, protocol string) string {
	class, subclass, protocol = normalizeClass(class), normalizeClass(subclass), normalizeClass(protocol)
	switch class {
	case "01":
		return "audio"
	case "02":
		switch {
		case subclass == "02":
			return "serial" // CDC Abstract Control Model (ACM)
		case cdcNetworkSubclasses[subclass]:
			return "network"
		default:
			return functionCDCControl
		}
	case "0a":
		// CDC Data does not say whether the owning function is ACM, ECM,
		// NCM or MBIM. It is associated below or remains explicitly generic.
		return functionCDCData
	case "03":
		return "hid"
	case "07":
		return "printer"
	case "08":
		return "storage"
	case "09":
		return "hub"
	case "0e":
		return "video"
	case "e0":
		return "network"
	case "fe":
		if subclass == "01" {
			return "dfu"
		}
	case "ff":
		return "vendor"
	}
	if class == "00" || class == "ef" || class == "" {
		return "unknown"
	}
	_ = protocol
	return "usb"
}

func classifyFunctions(functions []USBFunction) []USBFunction {
	classified := slices.Clone(functions)
	for index := range classified {
		function := &classified[index]
		function.Class = normalizeClass(function.Class)
		function.SubClass = normalizeClass(function.SubClass)
		function.Protocol = normalizeClass(function.Protocol)
		if function.ID == "" {
			function.ID = fmt.Sprintf("%d:%d.%d", function.Configuration, function.Interface, function.Alternate)
		}
		if function.Class == "02" || function.Class == "0a" || function.Kind == "" {
			function.Kind = classifyDescriptor(function.Class, function.SubClass, function.Protocol)
		}
	}

	controls := make(map[[2]int]string)
	for _, function := range classified {
		if function.Class == "02" && (function.Kind == "serial" || function.Kind == "network") {
			key := [2]int{function.Configuration, function.Interface}
			if current, exists := controls[key]; exists && current != function.Kind {
				controls[key] = ""
			} else {
				controls[key] = function.Kind
			}
		}
	}
	for index := range classified {
		function := &classified[index]
		if function.Class != "0a" {
			continue
		}
		// Inventories without Union/IAD metadata can infer a standard adjacent
		// control/data pair. Never inherit across another unrelated interface.
		control := function.Interface - 1
		if function.CDCControl != nil {
			control = *function.CDCControl
		}
		if kind := controls[[2]int{function.Configuration, control}]; kind != "" {
			function.Kind = kind
			function.CDCControl = &control
		}
	}
	return classified
}
