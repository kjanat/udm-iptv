package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/installer"
)

var (
	errInvalidCurrent = errors.New("invalid current")
	errInvalidLegacy  = errors.New("invalid legacy")
)

type installTestBackend struct {
	plans   []installer.Plan
	actions []string
}

func (b *installTestBackend) record(action string, plan installer.Plan) error {
	b.plans = append(b.plans, plan)
	b.actions = append(b.actions, action)

	return nil
}

func (b *installTestBackend) Preflight(_ context.Context, plan installer.Plan) error {
	return b.record("Check installation prerequisites", plan)
}

func (b *installTestBackend) PreserveRuntime(_ context.Context, plan installer.Plan) error {
	return b.record("Preserve the proxy and shared libraries offline", plan)
}

func (b *installTestBackend) SaveConfig(_ context.Context, plan installer.Plan) error {
	return b.record("Save configuration", plan)
}

func (b *installTestBackend) RemoveLegacy(_ context.Context, plan installer.Plan) error {
	return b.record("Remove legacy Debian package if installed", plan)
}

func (b *installTestBackend) CopyBinary(_ context.Context, plan installer.Plan) error {
	return b.record("Install persistent executable", plan)
}

func (b *installTestBackend) WriteFiles(_ context.Context, plan installer.Plan) error {
	return b.record("Write service, links, tmpfiles rule and shell completion", plan)
}

func (b *installTestBackend) Activate(_ context.Context, plan installer.Plan) error {
	return b.record("Reload systemd, enable and restart service", plan)
}

func (b *installTestBackend) CheckHealth(_ context.Context, plan installer.Plan) error {
	return b.record("Wait for stable proxy readiness", plan)
}

func (b *installTestBackend) Cleanup(_ context.Context, plan installer.Plan) error {
	return b.record("Remove obsolete legacy recovery files after health verification", plan)
}

type installDependenciesBuilder struct {
	current      config.Config
	currentFound bool
	currentErr   error
	legacy       config.Config
	legacyFound  bool
	legacyErr    error
	defaults     func() config.Config
	prompt       func(context.Context, *config.Config) error
	promptFresh  func(context.Context, *config.Config) error
	executable   string
	requireRoot  func() error
	backend      installer.Backend
}

func (b installDependenciesBuilder) build() installDependencies {
	return installDependencies{
		load: func() (config.Config, error) {
			switch {
			case b.currentErr != nil:
				return config.Config{}, b.currentErr
			case b.currentFound:
				return b.current, nil
			default:
				return config.Config{}, os.ErrNotExist
			}
		},
		legacy: func() (config.Config, bool, error) {
			if b.legacyErr != nil {
				return config.Config{}, false, b.legacyErr
			}

			return b.legacy, b.legacyFound, nil
		},
		defaults:    b.defaults,
		prompt:      b.prompt,
		promptFresh: b.promptFresh,
		executable:  func() (string, error) { return b.executable, nil },
		requireRoot: b.requireRoot,
		lock:        func(string) (func() error, error) { return func() error { return nil }, nil },
		backend:     b.backend,
	}
}

type installPreviewCase struct {
	name        string
	existing    bool
	interactive bool
	wantPrompts int
	wantFiles   int
}

func TestInstallDryRunUsesWizardWithoutSystemActions(t *testing.T) {
	for _, testCase := range []installPreviewCase{
		{name: "new", existing: false, interactive: false, wantPrompts: 0, wantFiles: 0},
		{name: "new-wizard", existing: false, interactive: true, wantPrompts: 1, wantFiles: 0},
		{name: "existing", existing: true, interactive: false, wantPrompts: 0, wantFiles: 1},
		{name: "existing-wizard", existing: true, interactive: true, wantPrompts: 1, wantFiles: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) { runInstallPreviewCase(t, testCase) })
	}
}

func runInstallPreviewCase(t *testing.T, testCase installPreviewCase) {
	t.Helper()
	directory := t.TempDir()
	var out bytes.Buffer
	application := &Application{ConfigPath: filepath.Join(directory, "config.json"), StateDir: directory, Out: &out, Err: &out}
	value := config.Default()
	value.Telemetry.Enabled = true
	if testCase.existing {
		if err := config.Save(application.ConfigPath, value); err != nil {
			t.Fatal(err)
		}
	}
	backend := &installTestBackend{}
	prompts := 0
	deps := installDependenciesBuilder{
		current: value, currentFound: testCase.existing,
		defaults: func() config.Config { return value },
		prompt: func(_ context.Context, draft *config.Config) error {
			prompts++
			draft.WAN.VLAN = 20

			return nil
		},
		promptFresh: func(context.Context, *config.Config) error {
			t.Fatal("dry-run called external detection")

			return nil
		},
		executable: "/tmp/preview-binary",
		requireRoot: func() error {
			t.Fatal("preview checked root")

			return nil
		},
		backend: backend,
	}
	root := &cobra.Command{Use: "udm-iptv"}
	root.AddCommand(application.installCommandWith(deps.build()))
	args := []string{"install", "--dry-run"}
	if !testCase.interactive {
		args = append(args, "--non-interactive")
	}
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if prompts != testCase.wantPrompts {
		t.Fatalf("preview invoked the wizard %d times", prompts)
	}
	if len(backend.actions) != 0 {
		t.Fatalf("preview ran system actions: %v", backend.actions)
	}
	if application.monitor != nil {
		t.Fatal("preview opened a reporter")
	}
	assertPreviewKeptFiles(t, application, testCase, value)
	assertPreviewOutput(t, out.String())
}

func assertPreviewKeptFiles(t *testing.T, application *Application, testCase installPreviewCase, value config.Config) {
	t.Helper()
	files, err := os.ReadDir(application.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != testCase.wantFiles {
		t.Fatalf("preview wrote files: %v", files)
	}
	if !testCase.existing {
		return
	}
	saved, err := config.Load(application.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.WAN.VLAN != value.WAN.VLAN {
		t.Fatal("preview changed saved configuration")
	}
}

func assertPreviewOutput(t *testing.T, output string) {
	t.Helper()
	if strings.Contains(output, "telemetry is unavailable") {
		t.Fatal("preview initialized telemetry")
	}
	if !strings.Contains(output, "Preview complete") {
		t.Fatal(output)
	}
}

type installSelectionCase struct {
	name           string
	currentFound   bool
	currentErr     error
	legacyFound    bool
	legacyErr      error
	promptErr      error
	promptVLAN     int
	rootErr        error
	wantErr        error
	wantSuccess    bool
	wantSaveConfig bool
	wantVLAN       int
}

func installSelectionCases() []installSelectionCase {
	return []installSelectionCase{
		{name: "existing", currentFound: true, wantSuccess: true, wantSaveConfig: false, wantVLAN: 20},
		{name: "legacy", legacyFound: true, wantSuccess: true, wantSaveConfig: true, wantVLAN: 20},
		{name: "defaults", wantSuccess: true, wantSaveConfig: true, wantVLAN: 4},
		{name: "invalid-current", currentErr: errInvalidCurrent, wantErr: errInvalidCurrent},
		{name: "invalid-legacy", legacyErr: errInvalidLegacy, wantErr: errInvalidLegacy},
		{name: "cancel-form", promptErr: context.Canceled, wantErr: context.Canceled},
		{name: "invalid-form", promptVLAN: -1},
		{name: "not-root", rootErr: os.ErrPermission, wantErr: os.ErrPermission},
	}
}

func TestInstallSelectionAndFailures(t *testing.T) {
	for _, testCase := range installSelectionCases() {
		t.Run(testCase.name, func(t *testing.T) { runInstallSelectionCase(t, testCase) })
	}
}

func runInstallSelectionCase(t *testing.T, testCase installSelectionCase) {
	t.Helper()
	var out bytes.Buffer
	application := &Application{ConfigPath: "/data/config.json", StateDir: "/data/iptv", Out: &out, Err: &out}
	backend := &installTestBackend{}
	value := config.Default()
	value.WAN.VLAN = 20
	deps := installDependenciesBuilder{
		current: value, currentFound: testCase.currentFound, currentErr: testCase.currentErr,
		legacy: value, legacyFound: testCase.legacyFound, legacyErr: testCase.legacyErr,
		defaults: config.DefaultKPN,
		prompt: func(_ context.Context, draft *config.Config) error {
			if testCase.promptVLAN != 0 {
				draft.WAN.VLAN = testCase.promptVLAN
			}

			return testCase.promptErr
		},
		executable:  "/tmp/installer",
		requireRoot: func() error { return testCase.rootErr },
		backend:     backend,
	}
	command := application.installCommandWith(deps.build())
	command.SetArgs(nil)
	err := command.Execute()
	assertInstallOutcome(t, testCase, err, out.String(), backend)
	if !testCase.wantSuccess {
		return
	}
	plan := backend.plans[0]
	if plan.SaveConfig != testCase.wantSaveConfig {
		t.Fatal("wrong persistence policy")
	}
	if plan.Config.WAN.VLAN != testCase.wantVLAN {
		t.Fatal("wrong configuration selected")
	}
}

func assertInstallOutcome(t *testing.T, testCase installSelectionCase, err error, output string, backend *installTestBackend) {
	t.Helper()
	if (err == nil) != testCase.wantSuccess {
		t.Fatalf("unexpected result: %v", err)
	}
	if testCase.wantErr != nil && !errors.Is(err, testCase.wantErr) {
		t.Fatalf("unexpected error: %v", err)
	}
	if (len(backend.actions) != 0) != testCase.wantSuccess {
		t.Fatalf("failed preparation applied installation: %v", backend.actions)
	}
	if strings.Contains(output, "has started") != testCase.wantSuccess {
		t.Fatal(output)
	}
}

func TestLegacyCandidatesIncludeThePersistedBackup(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	candidates := legacyCandidates(stateDir)
	if got := candidates[len(candidates)-1]; got != filepath.Join(stateDir, "udm-iptv.conf") {
		t.Fatalf("persisted backup is not a candidate: %q", candidates)
	}
	backup := `IPTV_WAN_INTERFACE="eth9"
IPTV_WAN_VLAN="35"
IPTV_WAN_VLAN_INTERFACE="iptv"
IPTV_WAN_RANGES="198.51.100.0/24"
IPTV_LAN_INTERFACES="br0"
IPTV_IGMPPROXY_PROGRAM="improxy"
IPTV_IGMPPROXY_IGMP_VERSION="3"
`
	if err := atomicfile.Write(filepath.Join(stateDir, "udm-iptv.conf"), []byte(backup), 0o600); err != nil {
		t.Fatal(err)
	}
	value, found, err := config.ImportFirstLegacy(candidates)
	if err != nil || !found {
		t.Fatalf("persisted backup not imported: found=%t err=%v", found, err)
	}
	if value.WAN.Interface != "eth9" || value.WAN.VLAN != 35 {
		t.Fatalf("imported %#v", value.WAN)
	}
}

// preremove runs inside apt, which holds the dpkg lock, so the flag it passes
// must reach the removal without the CLI calling the package manager again.
func TestPreremoveUsesTheInternalRemovalPath(t *testing.T) {
	t.Parallel()
	script, err := os.ReadFile(filepath.Join("..", "..", "packaging", "deb", "preremove"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "uninstall --keep-config --from-package") {
		t.Fatalf("preremove does not use the internal removal path:\n%s", script)
	}
	command := (&Application{}).uninstallCommand()
	flag := command.Flags().Lookup("from-package")
	if flag == nil {
		t.Fatal("uninstall has no --from-package flag for maintainer scripts")
	}
	if !flag.Hidden {
		t.Fatal("--from-package is offered to users")
	}
	if flag.DefValue != "false" {
		t.Fatalf("--from-package defaults to %q, so an ordinary uninstall skips delegation", flag.DefValue)
	}
}
