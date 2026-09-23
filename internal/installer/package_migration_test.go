package installer

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

type packageMigrationCase struct {
	name, action, version, installed, stopExit string
	goPackage, noConfig, savedJSON, wantError  bool
	wantStop, wantPending                      bool
}

const (
	packageMigrationLegacy = "IPTV_WAN_INTERFACE=eth8\nIPTV_WAN_VLAN=4\nIPTV_WAN_RANGES=213.75.0.0/16\n"
	packageMigrationJSON   = "existing JSON must survive\n"
)

func TestPackagePreinstallQuiescesLegacyBeforeHandover(t *testing.T) {
	t.Parallel()
	for _, test := range []packageMigrationCase{
		{name: "v4 upgrade", action: "upgrade", version: "4.3.2", installed: "4.3.2", wantStop: true, wantPending: true},
		{name: "saved JSON", action: "upgrade", version: "4.3.2", installed: "4.3.2", savedJSON: true, wantStop: true, wantPending: true},
		{name: "fresh install", action: "install"},
		{name: "Go upgrade", action: "upgrade", version: "5.0.0~preview.6", goPackage: true},
		{name: "Go marker", action: "upgrade", version: "4.3.2", goPackage: true},
		{name: "changed version", action: "upgrade", version: "4.3.2", installed: "4.3.1", wantError: true},
		{name: "missing config", action: "upgrade", version: "4.3.2", installed: "4.3.2", noConfig: true, wantError: true},
		{name: "stop fails", action: "upgrade", version: "4.3.2", installed: "4.3.2", stopExit: "1", wantStop: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := preparePackageMigration(t, test)
			script := packageMigrationScript(t, directory)
			command := exec.CommandContext(t.Context(), "sh", "-c", script, "preinstall", test.action, test.version)
			command.Env = append(os.Environ(), "TEST_DIRECTORY="+directory, "TEST_INSTALLED="+test.installed, "TEST_STOP_EXIT="+test.stopExit)
			output, runErr := command.CombinedOutput()
			if (runErr != nil) != test.wantError {
				t.Fatalf("preinstall: %v\n%s", runErr, output)
			}
			assertPackageMigration(t, directory, test)
			if test.savedJSON {
				assertMigrationJSONPreserved(t, directory)
			}
		})
	}
}

func preparePackageMigration(t *testing.T, test packageMigrationCase) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"daemon": "#!/bin/sh\nexit 0\n",
		"query":  "#!/bin/sh\nprintf '%s\\n' \"$TEST_INSTALLED\"\n",
		"systemctl": `#!/bin/sh
set -eu
test "$*" = 'stop udm-iptv.service'
test -x "$TEST_DIRECTORY/daemon"
test ! -e "$TEST_DIRECTORY/state/legacy-network.pending"
printf '%s\n' stopped > "$TEST_DIRECTORY/stopped"
exit "${TEST_STOP_EXIT:-0}"
`,
	}
	if !test.noConfig {
		files["legacy.conf"] = packageMigrationLegacy
	}
	if test.goPackage {
		files["go-package"] = "Go package\n"
	}
	if test.savedJSON {
		files["state/config.json"] = packageMigrationJSON
	}
	for name, contents := range files {
		if err := atomicfile.Write(filepath.Join(directory, name), []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func packageMigrationScript(t *testing.T, directory string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "packaging", "deb", "preinstall"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReplacer(
		"/data/udm-iptv", filepath.Join(directory, "state"),
		"/etc/udm-iptv.conf", filepath.Join(directory, "legacy.conf"),
		"/usr/share/udm-iptv/go-package", filepath.Join(directory, "go-package"),
		"/usr/lib/udm-iptv/udm-iptvd", filepath.Join(directory, "daemon"),
		"/run/systemd/system", directory,
		"dpkg-query", filepath.Join(directory, "query"),
		"systemctl stop", filepath.Join(directory, "systemctl")+" stop",
	).Replace(string(data))
}

func assertPackageMigration(t *testing.T, directory string, test packageMigrationCase) {
	t.Helper()
	state := filepath.Join(directory, "state")
	_, stopErr := os.Stat(filepath.Join(directory, "stopped"))
	if (stopErr == nil) != test.wantStop {
		t.Fatalf("service stop: %v, wanted %t", stopErr, test.wantStop)
	}
	pending, pendingErr := os.ReadFile(filepath.Join(state, "legacy-network.pending"))
	if test.wantPending {
		if pendingErr != nil || string(pending) != packageMigrationLegacy {
			t.Fatalf("handover changed old configuration: %q, %v", pending, pendingErr)
		}
	} else if !errors.Is(pendingErr, os.ErrNotExist) {
		t.Fatalf("unexpected handover: %q, %v", pending, pendingErr)
	}
	leftovers, err := filepath.Glob(filepath.Join(state, "legacy-network.*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leftover := range leftovers {
		if filepath.Base(leftover) != "legacy-network.pending" {
			t.Errorf("temporary handover left behind: %s", leftover)
		}
	}
}

func assertMigrationJSONPreserved(t *testing.T, directory string) {
	t.Helper()
	preserved, err := os.ReadFile(filepath.Join(directory, "state", "config.json"))
	if err != nil || string(preserved) != packageMigrationJSON {
		t.Fatalf("saved JSON changed: %q, %v", preserved, err)
	}
}
