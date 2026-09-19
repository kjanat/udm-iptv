package config

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func TestTelemetryDefaultsAndExistingChoice(t *testing.T) {
	value := withPorts(Default())
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

func TestOmittedTelemetryUsesDefaults(t *testing.T) {
	t.Parallel()
	want := defaultTelemetry()
	for _, testCase := range []struct {
		name string
		raw  string
	}{
		{name: "missing", raw: `{"profile":"custom","wan":{"interface":"eth8","vlan":0,"vlanInterface":"iptv"},"lan":{"interfaces":["br0"]},"proxy":{"program":"improxy","igmpVersion":3}}`},
		{name: "empty", raw: `{"profile":"custom","wan":{"interface":"eth8","vlan":0,"vlanInterface":"iptv"},"lan":{"interfaces":["br0"]},"proxy":{"program":"improxy","igmpVersion":3},"telemetry":{}}`},
		{name: "null", raw: `{"profile":"custom","wan":{"interface":"eth8","vlan":0,"vlanInterface":"iptv"},"lan":{"interfaces":["br0"]},"proxy":{"program":"improxy","igmpVersion":3},"telemetry":null}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			value, err := decodeConfig([]byte(testCase.raw))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(value.Telemetry, want) {
				t.Fatalf("telemetry = %+v, want %+v", value.Telemetry, want)
			}
		})
	}
	partial, err := decodeConfig([]byte(`{"profile":"custom","wan":{"interface":"eth8","vlan":0,"vlanInterface":"iptv"},"lan":{"interfaces":["br0"]},"proxy":{"program":"improxy","igmpVersion":3},"telemetry":{"enabled":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	wantOff := want
	wantOff.Enabled = false
	if !reflect.DeepEqual(partial.Telemetry, wantOff) {
		t.Fatalf("partial overlay = %+v, want %+v", partial.Telemetry, wantOff)
	}
}

func TestMigratedConfigurationKeepsDefaultTelemetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.conf")
	// The packaging always wrote IPTV_WAN_RANGES, and igmpproxy needs a source
	// prefix, so a realistic legacy file carries one.
	legacy := "IPTV_WAN_INTERFACE=eth8\nIPTV_WAN_RANGES=213.75.0.0/16\n"
	if err := atomicfile.Write(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := ImportLegacy(path)
	if err != nil {
		t.Fatal(err)
	}
	if !value.Telemetry.Enabled {
		t.Fatal("migration disabled telemetry")
	}
}
