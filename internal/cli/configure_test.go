package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

var (
	errTestProxyConflict = errors.New("UniFi IGMP Proxy enabled")
	errTestActivation    = errors.New("service failed")
)

func TestConfigureSetAppliesFlagsAfterLoading(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	current := configtest.Custom()
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

func TestConfigureConflictPreservesConfiguration(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	if err := config.Save(path, configtest.KPN()); err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	application := &Application{
		ConfigPath: path, StateDir: filepath.Join(directory, "state"), Out: &output, Err: &output,
		proxyPreflight: func(context.Context) error { return errTestProxyConflict },
	}
	command := application.root()
	command.SetArgs([]string{"configure", "set", "--quickleave=true"})
	if err := command.Execute(); !errors.Is(err, errTestProxyConflict) {
		t.Fatalf("configure: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(previous, after) {
		t.Fatalf("configuration changed: %q, %v", after, err)
	}
	if _, err := os.Stat(path + rejectedSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected rejected configuration: %v", err)
	}
	if application.reportConfig != nil || strings.Contains(output.String(), "Configuration saved") {
		t.Fatalf("reported a configuration change: %s", &output)
	}
}

func TestLifecycleConflictDoesNotReachSystemd(t *testing.T) {
	t.Parallel()
	application := &Application{proxyPreflight: func(context.Context) error { return errTestProxyConflict }}
	if err := application.restart(t.Context(), true); !errors.Is(err, errTestProxyConflict) {
		t.Fatalf("restart reached systemd despite conflict: %v", err)
	}
	if err := application.start(t.Context()); !errors.Is(err, errTestProxyConflict) {
		t.Fatalf("start reached systemd despite conflict: %v", err)
	}
}

func TestFailedActivationDoesNotReportSavedConfiguration(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	application := &Application{Out: &output}
	err := application.finishConfiguration(t.Context(), true, nil, func(context.Context, bool) error { return errTestActivation })
	if !errors.Is(err, errTestActivation) || strings.Contains(output.String(), "Configuration saved") {
		t.Fatalf("error=%v output=%s", err, &output)
	}
}

func seedBR0(value config.Config) config.Config {
	value.WAN.Interface = "eth8"
	value.LAN.Interfaces = []string{"br0"}

	return value
}

// Switching profile keeps the interfaces a saved configuration holds.
func TestConfigureSetKeepsSavedInterfaces(t *testing.T) {
	t.Parallel()
	for profile, want := range map[string][]string{config.ProfileCustom: {"br20", "br30"}, config.ProfileKPN: {"br20", "br30"}} {
		directory := t.TempDir()
		path := filepath.Join(directory, "config.json")
		current := configtest.KPN()
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

func TestRecoveryRestartsAfterCancellationAndOutputFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	previous := []byte(`{"profile":"kpn"}`)
	if err := atomicfile.Write(path, []byte(`{"profile":"custom"}`), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	restarted := false
	restart := func(recovery context.Context, check bool) error {
		restarted = true
		assertRecoveredConfiguration(recovery, t, check, path, previous)
		return nil
	}
	reader, writer := io.Pipe()
	_ = reader.Close()
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	})
	err := recoverConfiguration(ctx, path, previous, writer, restart)
	if !restarted || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("restarted=%t, error=%v", restarted, err)
	}
}

func TestConfigurationOutputFailureDoesNotSkipActivation(t *testing.T) {
	t.Parallel()
	reader, writer := io.Pipe()
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	})
	application := &Application{Out: writer, ConfigPath: filepath.Join(t.TempDir(), "config.json")}
	restarted := false
	err := application.finishConfiguration(t.Context(), true, nil, func(_ context.Context, check bool) error {
		restarted = check
		return nil
	})
	if !restarted || !application.reportApplied || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("restart=%t applied=%t error=%v", restarted, application.reportApplied, err)
	}
}

func assertRecoveredConfiguration(recovery context.Context, t *testing.T, check bool, path string, previous []byte) {
	t.Helper()
	if recovery.Err() != nil || !check {
		t.Fatalf("recovery inherited cancellation or skipped health: %v, %t", recovery.Err(), check)
	}
	deadline, ok := recovery.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Minute {
		t.Fatalf("recovery deadline: %v, %t", deadline, ok)
	}
	restored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(restored, previous) {
		t.Fatalf("restart saw %q: %v", restored, err)
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
	current := configtest.KPN()
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
