package platform

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	ShareAllow   = "allow"
	ShareConfirm = "confirm"
	ShareBlock   = "block"

	ReconnectAutomatic = "automatic"
	ReconnectConfirmed = "confirmed"
)

var stLinkProducts = map[string]bool{
	"3748": true, "374b": true, "374d": true, "374e": true,
	"374f": true, "3752": true, "3753": true, "3754": true,
}

func normalizeClass(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(value), "0x"))
	if len(value) == 1 {
		value = "0" + value
	}
	return value
}

func functionKind(class, subclass, protocol string) string {
	class, subclass, protocol = normalizeClass(class), normalizeClass(subclass), normalizeClass(protocol)
	switch class {
	case "01":
		return "audio"
	case "02", "0a":
		return "serial"
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

func EnrichDevice(device Device) Device {
	device.DeviceClass = normalizeClass(device.DeviceClass)
	device.DeviceSubClass = normalizeClass(device.DeviceSubClass)
	device.DeviceProtocol = normalizeClass(device.DeviceProtocol)
	profiles := make([]string, 0, len(device.Functions)+2)
	for i := range device.Functions {
		function := &device.Functions[i]
		function.Class = normalizeClass(function.Class)
		function.SubClass = normalizeClass(function.SubClass)
		function.Protocol = normalizeClass(function.Protocol)
		if function.Kind == "" {
			function.Kind = functionKind(function.Class, function.SubClass, function.Protocol)
		}
		if function.ID == "" {
			function.ID = fmt.Sprintf("%d:%d.%d", function.Configuration, function.Interface, function.Alternate)
		}
		profiles = append(profiles, function.Kind)
	}
	if len(device.Functions) == 0 {
		profiles = append(profiles, functionKind(device.DeviceClass, device.DeviceSubClass, device.DeviceProtocol))
	}

	vid, pid := strings.ToLower(device.VendorID), strings.ToLower(device.ProductID)
	label := strings.ToLower(device.Manufacturer + " " + device.Product)
	switch {
	case strings.Contains(label, "usb hub") || strings.Contains(label, "root hub"):
		profiles = append(profiles, "hub")
	case vid == "0483" && (stLinkProducts[pid] || strings.Contains(label, "st-link") || strings.Contains(label, "stlink")):
		profiles = append(profiles, "debug-probe", "stlink")
	case vid == "1366" || strings.Contains(label, "j-link") || strings.Contains(label, "jlink"):
		profiles = append(profiles, "debug-probe", "jlink")
	case strings.Contains(label, "cmsis-dap") || strings.Contains(label, "daplink"):
		profiles = append(profiles, "debug-probe", "cmsis-dap")
	case vid == "303a" && pid == "1001":
		profiles = append(profiles, "debug-probe", "serial")
	}

	profiles = compactStrings(profiles)
	if len(profiles) == 0 || (len(profiles) == 1 && profiles[0] == "") {
		profiles = []string{"unknown"}
	}
	device.Profiles = profiles
	device.SharePolicy, device.ReconnectPolicy = ShareAllow, ReconnectAutomatic
	for _, profile := range profiles {
		switch profile {
		case "hub":
			device.Risk, device.SharePolicy, device.ReconnectPolicy = "topology", ShareBlock, ReconnectConfirmed
		case "storage":
			device.Risk, device.SharePolicy, device.ReconnectPolicy = "storage", ShareConfirm, ReconnectConfirmed
		case "hid":
			if device.Risk == "" {
				device.Risk, device.SharePolicy, device.ReconnectPolicy = "input", ShareConfirm, ReconnectConfirmed
			}
		case "network":
			device.Risk, device.SharePolicy, device.ReconnectPolicy = "network", ShareConfirm, ReconnectConfirmed
		case "audio", "video":
			if device.Risk == "" {
				device.Risk, device.SharePolicy, device.ReconnectPolicy = "timing", ShareConfirm, ReconnectConfirmed
			}
		}
	}
	return device
}

func RequiresRiskConfirmation(device Device) bool { return device.SharePolicy == ShareConfirm }

func FinalizeImport(imported Import) Import {
	sort.Slice(imported.Functions, func(i, j int) bool { return imported.Functions[i].ID < imported.Functions[j].ID })
	sort.Slice(imported.Bindings, func(i, j int) bool {
		if imported.Bindings[i].Kind != imported.Bindings[j].Kind {
			return imported.Bindings[i].Kind < imported.Bindings[j].Kind
		}
		return imported.Bindings[i].ID < imported.Bindings[j].ID
	})
	imported.Bindings = slices.CompactFunc(imported.Bindings, func(a, b LocalBinding) bool {
		return a.Function == b.Function && a.Kind == b.Kind && a.ID == b.ID
	})
	for _, binding := range imported.Bindings {
		status := strings.ToLower(binding.Status)
		if binding.Kind == "serial" && binding.ID != "" && (status == "ready" || status == "") {
			imported.SerialPorts = append(imported.SerialPorts, binding.ID)
		}
	}
	imported.SerialPorts = compactStrings(imported.SerialPorts)

	ready, missing, failed := 0, 0, 0
	for _, binding := range imported.Bindings {
		status := strings.ToLower(binding.Status)
		switch {
		case binding.Problem != "" || status == "error" || status == "failed":
			failed++
		case status == "driver_missing" || status == "missing":
			missing++
		case binding.Kind != "usb" || binding.Driver != "":
			ready++
		}
	}
	switch {
	case (failed > 0 || missing > 0) && ready > 0:
		imported.Readiness = "degraded"
	case failed > 0:
		imported.Readiness = "error"
	case missing > 0:
		imported.Readiness = "driver_missing"
	case ready > 0:
		imported.Readiness = "ready"
	case len(imported.Bindings) > 0:
		imported.Readiness = "unknown"
	case imported.State != "":
		imported.Readiness = "attached"
	default:
		imported.Readiness = "unknown"
	}
	return imported
}

func compactStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func ParseDecimalAttribute(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	return n
}

func ParseHexAttribute(value string) int {
	value = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(value)), "0x")
	n, _ := strconv.ParseInt(value, 16, 32)
	return int(n)
}

func ImportBindingCheck(imports []Import, err error) Check {
	check := Check{Name: "client USB bindings"}
	if err != nil {
		check.Detail = "could not inspect native USB/IP imports: " + err.Error()
		return check
	}
	if len(imports) == 0 {
		check.OK, check.Detail = true, "no active imports; binding readiness is not applicable"
		return check
	}
	ready, attention := 0, 0
	for _, imported := range imports {
		switch imported.Readiness {
		case "ready":
			ready++
		default:
			attention++
		}
	}
	check.OK = attention == 0
	check.Detail = fmt.Sprintf("%d ready, %d need inspection across %d native imports", ready, attention, len(imports))
	return check
}
