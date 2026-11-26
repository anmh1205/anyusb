package platform

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestNativeRunnerCapturesExitAndBoundsOutput(t *testing.T) {
	result, err := (OSNativeRunner{}).Run(t.Context(), CommandRequest{
		Path: os.Args[0], Args: []string{"-test.run=TestNativeRunnerHelper", "--", "exit"},
		Environment: []string{"ANYUSB_COMMAND_HELPER=1"}, OutputLimit: 32, Timeout: 5 * time.Second,
	})
	var commandError *CommandError
	if !errors.As(err, &commandError) || commandError.Code != "exit" || result.ExitCode != 7 || !result.Truncated ||
		len(result.Stdout)+len(result.Stderr) > 32 {
		t.Fatalf("command result = %+v, error = %#v", result, err)
	}
}

func TestNativeRunnerRejectsRelativeOverride(t *testing.T) {
	_, err := (OSNativeRunner{}).Run(t.Context(), CommandRequest{Path: "relative/tool"})
	var commandError *CommandError
	if !errors.As(err, &commandError) || commandError.Code != "invalid_path" || !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative command error = %#v", err)
	}
}

func TestSensitiveNativeOutputStaysOutOfDiagnostics(t *testing.T) {
	result, _ := (OSNativeRunner{}).Run(t.Context(), CommandRequest{
		Tool: "sensitive-test-fixture", Path: os.Args[0], Args: []string{"-test.run=TestNativeRunnerHelper"},
		Environment: []string{"ANYUSB_COMMAND_HELPER=1"}, SensitiveOutput: true,
	})
	if result.Stdout == "" || result.Stderr == "" {
		t.Fatal("caller lost command output")
	}
	for _, diagnostic := range CommandDiagnostics() {
		if diagnostic.Tool == "sensitive-test-fixture" {
			if diagnostic.Stdout != "" || diagnostic.Stderr != "" || diagnostic.ExitCode != 7 {
				t.Fatalf("sensitive diagnostic = %+v", diagnostic)
			}
			return
		}
	}
	t.Fatal("command metadata missing from diagnostics")
}

func TestNativeRunnerHelper(t *testing.T) {
	if os.Getenv("ANYUSB_COMMAND_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString("stdout-abcdefghijklmnopqrstuvwxyz")
	_, _ = os.Stderr.WriteString("stderr-abcdefghijklmnopqrstuvwxyz")
	if os.Getenv("ANYUSB_COMMAND_OK") == "1" {
		os.Exit(0)
	}
	os.Exit(7)
}

func TestRoutinePollsSkipDiagnosticOnOKButRecordErrors(t *testing.T) {
	_, err := (OSNativeRunner{}).Run(t.Context(), CommandRequest{
		Tool: "routine-ok-fixture", Path: os.Args[0], Args: []string{"-test.run=TestNativeRunnerHelper"},
		Environment: []string{"ANYUSB_COMMAND_HELPER=1", "ANYUSB_COMMAND_OK=1"}, SkipDiagnosticOnOK: true,
	})
	if err != nil {
		t.Fatalf("routine ok command failed: %v", err)
	}
	_, _ = (OSNativeRunner{}).Run(t.Context(), CommandRequest{
		Tool: "routine-err-fixture", Path: os.Args[0], Args: []string{"-test.run=TestNativeRunnerHelper"},
		Environment: []string{"ANYUSB_COMMAND_HELPER=1"}, SkipDiagnosticOnOK: true,
	})
	var sawErr bool
	for _, diagnostic := range CommandDiagnostics() {
		if diagnostic.Tool == "routine-ok-fixture" {
			t.Fatalf("routine OK command polluted diagnostics: %+v", diagnostic)
		}
		if diagnostic.Tool == "routine-err-fixture" {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("failed routine poll was omitted from diagnostics")
	}
}
