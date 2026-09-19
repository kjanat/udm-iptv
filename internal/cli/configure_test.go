package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/filemode"
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

func seedBR0(value config.Config) config.Config {
	value.LAN.Interfaces = []string{"br0"}

	return value
}

// Switching profile keeps the interfaces a saved configuration holds.
func TestConfigureSetKeepsSavedInterfaces(t *testing.T) {
	t.Parallel()
	for profile, want := range map[string][]string{config.ProfileCustom: {"br20", "br30"}, config.ProfileKPN: {"br20", "br30"}} {
		directory := t.TempDir()
		path := filepath.Join(directory, "config.json")
		current := config.DefaultKPN()
		current.Profile = config.ProfileCustom
		current.LAN.Interfaces = []string{"br20", "br30"}
		if err := config.Save(path, current); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		application := &Application{Version: "test", ConfigPath: path, StateDir: filepath.Join(directory, "state"), Out: &output, Err: &output, seed: seedBR0}
		command := application.root()
		command.SetArgs([]string{"configure", "set", "--profile", profile})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		updated, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(updated.LAN.Interfaces, want) || updated.Profile != profile {
			t.Fatalf("--profile %s saved %s with %v, want %v", profile, updated.Profile, updated.LAN.Interfaces, want)
		}
	}
}

func TestConfigureGetReportsAnUnconfiguredConsole(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	var output bytes.Buffer
	application := &Application{Version: "test", ConfigPath: filepath.Join(directory, "config.json"), StateDir: filepath.Join(directory, "state"), Out: &output, Err: &output}
	for _, args := range [][]string{{"configure", "get"}, {"configure", "get", "profile"}} {
		command := application.root()
		command.SetArgs(args)
		if err := command.Execute(); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
	if output.Len() != 0 {
		t.Fatalf("printed a configuration that does not exist: %q", output.String())
	}
}

// A configuration the service will not start with goes back to what ran
// before, and the rejected file stays beside it for inspection.
func TestRollbackConfigurationRestoresThePreviousFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	previous := []byte(`{"profile":"kpn"}` + "\n")
	attempted := []byte(`{"profile":"custom"}` + "\n")
	if err := atomicfile.Write(path, attempted, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	rejected, err := rollbackConfiguration(path, previous)
	if err != nil {
		t.Fatal(err)
	}
	if rejected != path+rejectedSuffix {
		t.Fatalf("rejected copy at %s", rejected)
	}
	restored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(restored, previous) {
		t.Fatalf("restored %q, %v", restored, err)
	}
	kept, err := os.ReadFile(rejected)
	if err != nil || !bytes.Equal(kept, attempted) {
		t.Fatalf("rejected copy %q, %v", kept, err)
	}
	info, err := os.Stat(rejected)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("rejected copy mode %v, %v", info.Mode(), err)
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
