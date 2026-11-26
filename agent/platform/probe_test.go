package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("ANYUSB_PROBE_TEST_HELPER") == "1" {
		name := strings.ToLower(filepath.Base(os.Args[0]))
		args := os.Args[1:]
		if len(args) > 0 && (args[0] == "--version" || args[0] == "-?") {
			fmt.Println("AnyUSB fake probe version 1.0")
			os.Exit(0)
		}
		switch {
		case strings.Contains(name, "stm32_programmer_cli") && len(args) == 1 && args[0] == "-l":
			fmt.Println("ST-LINK probe serial: 00-25-AB-CD")
		case strings.Contains(name, "jlink") && len(args) == 4 && args[0] == "-NoGui" && args[2] == "-CommandFile":
			command, err := os.ReadFile(args[3])
			if err != nil || string(command) != "ShowEmuList USB\nexit\n" {
				os.Exit(2)
			}
			fmt.Println("J-Link S/N: 00-25-AB-CD")
		case strings.Contains(name, "pyocd") && len(args) == 1 && args[0] == "list":
			fmt.Println("CMSIS-DAP unique ID: 00-25-AB-CD")
		default:
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestProfileProbeRejectsUnknownProfile(t *testing.T) {
	_, err := RunProfileProbe(t.Context(), "unknown", "123")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("probe error = %v", err)
	}
}

func TestContainsIdentifierNormalizesSeparators(t *testing.T) {
	if !containsIdentifier("Probe S/N: 00-25-AB-CD", "0025abcd") {
		t.Fatal("probe serial was not matched")
	}
	for _, output := range []string{"Probe S/N: X0025ABCD", "Probe S/N: 0025ABCD9", "unrelated output"} {
		if containsIdentifier(output, "0025abcd") {
			t.Fatalf("probe serial matched non-exact output %q", output)
		}
	}
	if containsIdentifier("Probe S/N: 123", "---") {
		t.Fatal("separator-only serial matched probe output")
	}
}

func TestMissingProfileToolIsStructuredAndOptional(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	result, err := RunProfileProbe(t.Context(), "cmsis-dap", "123")
	if err != nil || result.OK || result.Error == "" {
		t.Fatalf("missing tool result = %+v, %v", result, err)
	}
	checks := ProfileToolChecks()
	if len(checks) != 3 {
		t.Fatalf("profile checks = %+v", checks)
	}
	for _, check := range checks {
		if !check.Optional {
			t.Fatalf("profile check is not optional: %+v", check)
		}
	}
}

func TestProfileProbesRunReadOnlyNativeCommands(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	extension := ""
	jlink := "JLinkExe"
	if runtime.GOOS == "windows" {
		extension = ".exe"
		jlink = "JLink.exe"
	}
	for _, test := range []struct {
		profile, name string
	}{
		{"stlink", "STM32_Programmer_CLI" + extension},
		{"jlink", jlink},
		{"cmsis-dap", "pyocd" + extension},
	} {
		t.Run(test.profile, func(t *testing.T) {
			directory := t.TempDir()
			tool := filepath.Join(directory, test.name)
			if err := os.WriteFile(tool, program, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory)
			t.Setenv("ANYUSB_PROBE_TEST_HELPER", "1")
			result, err := RunProfileProbe(t.Context(), test.profile, "0025abcd")
			if err != nil || !result.OK || !result.Matched || result.Version != "AnyUSB fake probe version 1.0" || len(result.Command) == 0 {
				t.Fatalf("%s probe result = %+v, %v", test.profile, result, err)
			}
		})
	}
}
