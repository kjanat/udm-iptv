package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTelemetryDefaultsAndExistingChoice(t *testing.T) {
	value := Default()
	if !value.Telemetry.Enabled {
		t.Fatal("new configuration must enable telemetry")
	}
	value.Telemetry.Enabled = false
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, value); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Telemetry.Enabled {
		t.Fatal("existing opt-out lost")
	}
	selected, err := FromProfile("tweak", loaded)
	if err != nil || selected.Telemetry.Enabled {
		t.Fatal("profile switch lost opt-out")
	}
}

func TestLegacyTelemetryRemainsDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.conf")
	if err := os.WriteFile(path, []byte("IPTV_WAN_INTERFACE=eth8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := ImportLegacy(path)
	if err != nil {
		t.Fatal(err)
	}
	if value.Telemetry.Enabled {
		t.Fatal("migration enabled telemetry")
	}
}
