package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
)

func TestNonInteractiveConfigurationAppliesFlagsAfterLoading(t *testing.T) {
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
	command.SetArgs([]string{"configure", "--non-interactive", "--wan-interface", "eth9", "--quickleave=true"})
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

func TestNonInteractiveConfigurationRejectsUnknownProfile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	application := &Application{Version: "test", ConfigPath: filepath.Join(directory, "config.json"), StateDir: filepath.Join(directory, "state"), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	command := application.root()
	command.SetArgs([]string{"configure", "--non-interactive", "--profile", "missing"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown provider profile") {
		t.Fatalf("unexpected error: %v", err)
	}
}
