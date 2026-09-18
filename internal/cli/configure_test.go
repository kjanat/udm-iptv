package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
)

func TestConfigureSetAppliesFlagsAfterLoading(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	current := config.Default()
	current.WAN.Interface = "eth8"
	if err := config.Save(path, current); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	application := &Application{Version: "test", ConfigPath: path, StateDir: filepath.Join(directory, "state"), Out: &output, Err: &output}
	command := application.root()
	command.SetArgs([]string{"configure", "set", "--wan-interface", "eth9", "--quickleave=true"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	updated, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.WAN.Interface != "eth9" || !updated.Proxy.QuickLeave {
		t.Fatalf("flags were not applied: %#v", updated)
	}
}

func TestConfigureSetRejectsUnknownProfile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	application := &Application{Version: "test", ConfigPath: filepath.Join(directory, "config.json"), StateDir: filepath.Join(directory, "state"), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	command := application.root()
	command.SetArgs([]string{"configure", "set", "--profile", "missing"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown provider profile") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigureSetNeedsAFlag(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	application := &Application{Version: "test", ConfigPath: filepath.Join(directory, "config.json"), StateDir: filepath.Join(directory, "state"), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	command := application.root()
	command.SetArgs([]string{"configure", "set"})
	if err := command.Execute(); !errors.Is(err, errNothingToSet) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigureGetPrintsSettings(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	current := config.DefaultKPN()
	current.WAN.NATDestinations = []string{"213.75.0.0/16", "217.166.0.0/16"}
	if err := config.Save(path, current); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct{ args, want string }{
		{"nat-destination", "213.75.0.0/16,217.166.0.0/16\n"},
		{"dhcp", "true\n"},
		{"profile", "kpn\n"},
		{"wan-vlan", "4\n"},
	} {
		var output bytes.Buffer
		application := &Application{Version: "test", ConfigPath: path, StateDir: filepath.Join(directory, "state"), Out: &output, Err: &output}
		command := application.root()
		command.SetArgs([]string{"configure", "get", testCase.args})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if output.String() != testCase.want {
			t.Fatalf("get %s = %q, want %q", testCase.args, output.String(), testCase.want)
		}
	}
	var output bytes.Buffer
	application := &Application{Version: "test", ConfigPath: path, StateDir: filepath.Join(directory, "state"), Out: &output, Err: &output}
	command := application.root()
	command.SetArgs([]string{"configure", "get"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var printed config.Config
	if err := json.Unmarshal(output.Bytes(), &printed); err != nil {
		t.Fatal(err)
	}
	if printed.WAN.VLAN != current.WAN.VLAN || printed.Profile != "kpn" {
		t.Fatalf("printed %+v", printed)
	}
	command = application.root()
	command.SetArgs([]string{"configure", "get", "colour"})
	if err := command.Execute(); !errors.Is(err, errUnknownSetting) {
		t.Fatalf("err = %v", err)
	}
}
