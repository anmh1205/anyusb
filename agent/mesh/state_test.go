package mesh

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateStoreMigratesUnversionedConfig(t *testing.T) {
	directory := t.TempDir()
	legacy := []byte(`{"peers":{},"shares":{},"connections":{}}`)
	if err := os.WriteFile(filepath.Join(directory, "state.json"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := New(ctx, &testAdapter{}, directory, "migration", "", "127.0.0.1:3240"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored config
	if err := json.Unmarshal(data, &stored); err != nil || stored.Version != currentStateVersion {
		t.Fatalf("migrated state = %s, %v", data, err)
	}
}

func TestStateStorePreservesCorruptInputAndFailsClosed(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	corrupt := []byte(`{"peers":`)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(t.Context(), &testAdapter{}, directory, "corrupt", "", "127.0.0.1:3240")
	if err == nil || !strings.Contains(err.Error(), "corrupt state preserved") {
		t.Fatalf("corrupt state error = %v", err)
	}
	backups, globErr := filepath.Glob(path + ".corrupt-*")
	if globErr != nil || len(backups) != 1 {
		t.Fatalf("corrupt backups = %v, %v", backups, globErr)
	}
	backup, readErr := os.ReadFile(backups[0])
	if readErr != nil || string(backup) != string(corrupt) {
		t.Fatalf("corrupt backup = %q, %v", backup, readErr)
	}
	current, readErr := os.ReadFile(path)
	if readErr != nil || string(current) != string(corrupt) {
		t.Fatalf("canonical corrupt input was overwritten = %q, %v", current, readErr)
	}
}
