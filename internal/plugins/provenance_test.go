package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPluginUpdatePreservesLocalStateAndApprovedCodeAcrossRestart(t *testing.T) {
	manager, _ := testPluginManager(t)
	statePath := filepath.Join(manager.dir, "filesystem", "local-state.json")
	if err := os.WriteFile(statePath, []byte(`{"counter":7}`), 0600); err != nil {
		t.Fatal(err)
	}
	changed := testPluginManifest()
	changed.Version = "1.1.0"
	raw, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager.cfg.Sources["local"], "plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Command(pluginContext(), []string{"update", "filesystem", "local"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(statePath)
	if err != nil || string(got) != `{"counter":7}` {
		t.Fatalf("local state lost during update: %s %v", got, err)
	}
	record := manager.installed["filesystem"]
	if !record.Enabled || record.Version != "1.1.0" {
		t.Fatalf("activation/version not preserved: %+v", record)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(manager.cfg, manager.host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restarted.Close() }()
	if !restarted.Contains(namespace("filesystem", "write")) {
		t.Fatal("untracked local state incorrectly invalidated approved plugin code")
	}
}

func TestPluginBuiltinCollisionAndOverlongSchemaAreRejected(t *testing.T) {
	manager, _ := testPluginManager(t)
	if err := manager.SetReservedTools([]string{namespace("filesystem", "write")}); err == nil {
		t.Fatal("plugin silently overrode reserved builtin")
	}
	manifest := testPluginManifest()
	manifest.Name = "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"
	if err := manifest.Validate(); err == nil {
		t.Fatal("invalid/overlong plugin namespace accepted")
	}
	manifest = testPluginManifest()
	manifest.Tools = append(manifest.Tools, ToolDefinition{Name: "host_write", Description: "duplicate normalized name", Parameters: map[string]any{"type": "object"}})
	if err := manifest.Validate(); err == nil {
		t.Fatal("normalized tool collision accepted")
	}
}
