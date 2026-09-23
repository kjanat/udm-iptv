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
		udm-iptv/telemetry) RET=$TEST_TELEMETRY ;;
		*) return 1 ;;
	esac
}
db_fget() { RET=$TEST_SEEN; }
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

func runPackageConsentScript(t *testing.T, scriptName, state, profile, telemetry, seen string) string {
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
		"TEST_STATE="+state, "TEST_PROFILE="+profile, "TEST_TELEMETRY="+telemetry,
		"TEST_SEEN="+seen, "TEST_CALLS="+filepath.Join(directory, "calls"))
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("%s: %v\n%s", scriptName, runErr, output)
	}
	calls, err := os.ReadFile(filepath.Join(directory, "calls"))
	if err != nil {
		t.Fatal(err)
	}

	return string(calls)
}

func TestPackageFreshInstallRequiresTelemetryConsent(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, choice, seen, want string
	}{
		{name: "accepted", choice: "true", seen: "true", want: "true"},
		{name: "declined", choice: "false", seen: "true", want: "false"},
		{name: "unattended", choice: "false", seen: "false", want: "false"},
		{name: "unseen true is not consent", choice: "true", seen: "false", want: "false"},
		{name: "missing answer", want: "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := runPackageConsentScript(t, "postinstall", "fresh", "kpn", test.choice, test.seen)
			want := "configure get\nconfigure set --profile kpn --telemetry=" + test.want + "\ninstall --force --non-interactive\n"
			if calls != want {
				t.Fatalf("calls = %q; want %q", calls, want)
			}
		})
	}
}

func TestPackageMigrationAndUpgradeConsent(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ state, profile, want string }{
		{"legacy", "kpn", "configure get\nconfigure set --telemetry=false\ninstall --force --non-interactive\n"},
		{"saved", "kpn", "configure get\ninstall --force --non-interactive\n"},
		{"fresh", "custom", "configure get\n"},
	} {
		t.Run(test.state+test.profile, func(t *testing.T) {
			if calls := runPackageConsentScript(t, "postinstall", test.state, test.profile, "false", "true"); calls != test.want {
				t.Fatalf("calls = %q; want %q", calls, test.want)
			}
		})
	}
}

func TestPackageOffersConsentBeforeFreshInstall(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"fresh", "legacy", "saved"} {
		calls := runPackageConsentScript(t, "config", state, "kpn", "false", "false")
		asked := strings.Contains(calls, "high udm-iptv/telemetry\n")
		if asked != (state != "saved") {
			t.Fatalf("state %s: consent prompt calls = %q", state, calls)
		}
	}
}
