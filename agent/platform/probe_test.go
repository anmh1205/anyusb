package platform

import (
	"errors"
	"testing"
)

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
