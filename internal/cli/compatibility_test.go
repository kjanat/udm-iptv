package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/diagnostics"
)

func TestLegacyDiagnoseNamesAndJSONFormat(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, name := range []string{"diagnose", "diag"} {
		for _, format := range []string{formatJSON, formatJSONL} {
			t.Run(name+"/"+format, func(t *testing.T) {
				directory := t.TempDir()
				var output bytes.Buffer
				application := &Application{
					Version: "compatibility-test", ConfigPath: filepath.Join(directory, "config.json"),
					StateDir: directory, Out: &output, Err: io.Discard,
				}
				value := config.DefaultKPN()
				value.WAN.Interface = "test-wan"
				value.LAN.Interfaces = []string{"br0"}
				value.Telemetry.Enabled = false
				if err := config.Save(application.ConfigPath, value); err != nil {
					t.Fatal(err)
				}
				command := application.root()
				command.SetArgs([]string{name, "--format", format})
				if err := command.ExecuteContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				assertLegacySnapshot(t, &output, value.Profile, application.Version)
			})
		}
	}
}

func assertLegacySnapshot(t *testing.T, output io.Reader, profile, version string) {
	t.Helper()
	decoder := json.NewDecoder(output)
	var event diagnostics.Event
	if err := decoder.Decode(&event); err != nil {
		t.Fatalf("legacy format did not emit JSON: %v", err)
	}
	if event.Type != "snapshot" || event.Snapshot == nil || event.Snapshot.Version != version {
		t.Fatalf("legacy invocation did not collect a snapshot: %#v", event)
	}
	if event.Snapshot.Config.Profile != profile {
		t.Fatalf("snapshot omitted the actual configuration: %#v", event.Snapshot.Config)
	}
	if err := decoder.Decode(&event); !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected trailing output: %v", err)
	}
}

func TestUninstallRetentionFlagsReachPackageScripts(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the real uninstall command requires root; all package commands are isolated fixtures")
	}
	for _, test := range []struct {
		name, flag, action, keepData, forwarded string
	}{
		{"default", "", "purge", "false", "--keep-config"},
		{"purge", "--purge", "purge", "false", "--keep-config"},
		{"configuration", "--keep-config", "remove", "false", "--keep-config"},
		{"all-data", "--keep-data", "remove", "true", "--keep-data"},
		{"disabled", "--keep-data=false", "purge", "false", "--keep-config"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := uninstallPackageFixtures(t)
			application := &Application{
				ConfigPath: filepath.Join(directory, "missing.json"), StateDir: directory,
				Out: io.Discard, Err: io.Discard,
			}
			command := application.root()
			arguments := []string{"uninstall"}
			if test.flag != "" {
				arguments = append(arguments, test.flag)
			}
			command.SetArgs(arguments)
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{
				"apt-arguments": test.action + "\n-y\nudm-iptv\n",
				"keep-data":     test.keepData + "\n",
				"preremove":     "uninstall\n" + test.forwarded + "\n--from-package\n",
			} {
				data, err := os.ReadFile(filepath.Join(directory, name))
				if err != nil || string(data) != want {
					t.Fatalf("%s=%q, want %q: %v", name, data, want, err)
				}
			}
		})
	}
}

func TestUninstallRejectsConflictingRetentionFlags(t *testing.T) {
	for _, arguments := range [][]string{
		{"uninstall", "--purge", "--keep-data"},
		{"uninstall", "--purge", "--keep-config"},
		{"uninstall", "--keep-config", "--keep-data"},
	} {
		t.Run(strings.Join(arguments[1:], "/"), func(t *testing.T) {
			directory := uninstallPackageFixtures(t)
			application := &Application{ConfigPath: filepath.Join(directory, "missing.json"), StateDir: directory, Out: io.Discard, Err: io.Discard}
			command := application.root()
			command.SetArgs(arguments)
			if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "group") {
				t.Fatalf("conflicting retention flags did not fail validation: %v", err)
			}
			if _, err := os.Stat(filepath.Join(directory, "apt-arguments")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("conflicting flags reached package removal: %v", err)
			}
		})
	}
}

func TestUninstallRejectsDisabledPurgeBeforeRemoval(t *testing.T) {
	directory := uninstallPackageFixtures(t)
	application := &Application{ConfigPath: filepath.Join(directory, "missing.json"), StateDir: directory, Out: io.Discard, Err: io.Discard}
	command := application.root()
	command.SetArgs([]string{"uninstall", "--purge=false"})
	if err := command.ExecuteContext(t.Context()); !errors.Is(err, errDisabledPurge) {
		t.Fatalf("disabled purge was not rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "apt-arguments")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled purge reached package removal: %v", err)
	}
}

func TestCaptureFormatSelectionCreatesRequestedOutputs(t *testing.T) {
	for _, test := range []struct {
		name, format string
		text, json   bool
	}{
		{"default", "", true, true},
		{"text", formatText, true, false},
		{"json", formatJSON, false, true},
		{"jsonl", formatJSONL, false, true},
		{"both", formatBoth, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			application := &Application{StateDir: t.TempDir(), Out: io.Discard, Err: io.Discard}
			command := application.diagnoseCommand()
			arguments := []string{"--capture", "1s"}
			if test.format != "" {
				arguments = append(arguments, "--format", test.format)
			}
			command.SetArgs(arguments)
			// Execute real parsing/default selection and file preparation without
			// launching a detached capture worker from the test executable.
			command.RunE = func(command *cobra.Command, _ []string) error {
				options := selectedCaptureFiles(t, application, command)
				assertCaptureOutput(t, options.TextPath, test.text)
				assertCaptureOutput(t, options.JSONPath, test.json)
				return nil
			}
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func selectedCaptureFiles(t *testing.T, application *Application, command *cobra.Command) diagnostics.Options {
	t.Helper()
	format, err := command.Flags().GetString("format")
	if err != nil {
		t.Fatal(err)
	}
	options, _, err := application.prepareCaptureFiles(diagnostics.Options{Format: format, Capture: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return options
}

func assertCaptureOutput(t *testing.T, path string, expected bool) {
	t.Helper()
	if (path != "") != expected {
		t.Fatalf("capture output path=%q, expected file=%t", path, expected)
	}
	if !expected {
		return
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("capture output was not created: %v", err)
	}
}

func uninstallPackageFixtures(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	bin := filepath.Join(directory, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{
		"dpkg-query": "#!/bin/sh\nprintf 'installed\\t5.0.0\\n'\n",
		"apt-get": `#!/bin/sh
printf '%s\n' "$@" > "${TEST_PACKAGE_DIR}/apt-arguments"
printf '%s\n' "${UDM_IPTV_REMOVE_KEEP_DATA-}" > "${TEST_PACKAGE_DIR}/keep-data"
exec /bin/sh "${TEST_PACKAGE_DIR}/preremove-script" remove
`,
		"udm-iptv": "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"${TEST_PACKAGE_DIR}/preremove\"\n",
	}
	for name, script := range scripts {
		if err := atomicfile.Write(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "packaging", "deb", "preremove"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(data), "/data/udm-iptv/bin/udm-iptv", filepath.Join(bin, "udm-iptv"))
	if err := atomicfile.Write(filepath.Join(directory, "preremove-script"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TEST_PACKAGE_DIR", directory)
	// Ordinary removals must not inherit another caller's retention request.
	t.Setenv("UDM_IPTV_REMOVE_KEEP_DATA", "true")
	return directory
}
