package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

const packageDebconfStub = `db_get() {
	case "$1" in
		udm-iptv/profile) RET=$TEST_PROFILE ;;
		*) return 1 ;;
	esac
}
db_stop() { :; }
db_version() { :; }
db_capb() { :; }
db_input() { printf '%s\n' "$*" >> "$TEST_CALLS"; }
db_go() { :; }
`

const packageBinaryStub = `#!/bin/sh
printf '%s\n' "$*" >> "$TEST_CALLS"
if [ "$1 $2" = "configure get" ] && [ "$TEST_STATE" = fresh ]; then
	exit 3
fi
`

func runPackageScript(t *testing.T, scriptName, state, profile string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"confmodule": packageDebconfStub, "bin/udm-iptv": packageBinaryStub} {
		if err := atomicfile.Write(filepath.Join(directory, name), []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if state == "saved" {
		if err := atomicfile.Write(filepath.Join(directory, "config.json"), []byte("saved choice"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "packaging", "deb", scriptName))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.NewReplacer(
		"/data/udm-iptv", directory,
		"/usr/share/debconf/confmodule", filepath.Join(directory, "confmodule"),
		"/usr/local/bin/udm-iptv", filepath.Join(directory, "command"),
	).Replace(string(data))
	command := exec.CommandContext(t.Context(), "sh", "-ec", script)
	command.Env = append(os.Environ(),
		"TEST_STATE="+state, "TEST_PROFILE="+profile,
		"TEST_CALLS="+filepath.Join(directory, "calls"))
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("%s: %v\n%s", scriptName, runErr, output)
	}
	calls, err := os.ReadFile(filepath.Join(directory, "calls"))
	if err != nil {
		t.Fatal(err)
	}

	return string(calls)
}

func TestPackagePreservesTelemetryDefaultsAndChoices(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ state, profile, want string }{
		{"fresh", "kpn", "configure get\nconfigure set --profile kpn\ninstall --force --non-interactive\n"},
		{"legacy", "kpn", "configure get\ninstall --force --non-interactive\n"},
		{"saved", "kpn", "configure get\ninstall --force --non-interactive\n"},
		{"fresh", "custom", "configure get\n"},
	} {
		t.Run(test.state+test.profile, func(t *testing.T) {
			if calls := runPackageScript(t, "postinstall", test.state, test.profile); calls != test.want {
				t.Fatalf("calls = %q; want %q", calls, test.want)
			}
		})
	}
}

func TestPackageAsksForProfile(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"fresh", "legacy", "saved"} {
		calls := runPackageScript(t, "config", state, "kpn")
		if calls != "high udm-iptv/profile\n" {
			t.Fatalf("state %s: prompt calls = %q", state, calls)
		}
	}
}
