package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	systemd "github.com/coreos/go-systemd/v22/dbus"
)

var errInjectedUninstallStep = errors.New("injected uninstall failure")

type uninstallBus struct {
	uninstallConnection

	calls     []string
	failTimer bool
}

func (b *uninstallBus) StopUnitContext(_ context.Context, unit, _ string, result chan<- string) (int, error) {
	b.calls = append(b.calls, "stop "+unit)
	if b.failTimer && unit == "udm-iptv.timer" {
		return 0, errInjectedUninstallStep
	}
	result <- "done"
	return 1, nil
}

func (b *uninstallBus) DisableUnitFilesContext(context.Context, []string, bool) ([]systemd.DisableUnitFileChange, error) {
	b.calls = append(b.calls, "disable")
	return nil, nil
}

func TestUninstallCancelsResumeBeforeRemovingService(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		bus := &uninstallBus{failTimer: fail}
		remover := uninstaller{connection: bus}
		step := func(name string) func(context.Context) error {
			return func(context.Context) error { bus.calls = append(bus.calls, name); return nil }
		}
		err := executeUninstall(t.Context(), uninstallActions{
			stop: remover.stopService, disable: remover.disableService, removeNAT: step("NAT"),
			removeFiles: step("files"), removeState: step("state"), reload: step("reload"),
		})
		want := []string{"stop udm-iptv.timer", "stop udm-iptv.service", "disable", "NAT", "files", "state", "reload"}
		if fail {
			want = want[:1]
		}
		if !reflect.DeepEqual(bus.calls, want) || errors.Is(err, errInjectedUninstallStep) != fail || (err != nil) != fail {
			t.Fatalf("calls=%v err=%v", bus.calls, err)
		}
	}
}

func TestUninstallFailureBoundaries(t *testing.T) {
	all := []string{"stop", "disable", "NAT", "files", "state", "reload"}
	for _, test := range []struct {
		name, fail string
		want       []string
		fails      bool
	}{
		{"success", "", all, false},
		{"stop", "stop", all[:1], true},
		{"disable", "disable", all[:2], true},
		{"NAT", "NAT", all, true},
		{"files", "files", all[:4], true},
		{"state", "state", all[:5], true},
		{"reload", "reload", all[:6], true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			cause := errInjectedUninstallStep
			step := func(name string) func(context.Context) error {
				return func(context.Context) error {
					calls = append(calls, name)
					if name == test.fail {
						return cause
					}

					return nil
				}
			}
			err := executeUninstall(t.Context(), uninstallActions{
				stop: step("stop"), disable: step("disable"), removeNAT: step("NAT"),
				removeFiles: step("files"), removeState: step("state"), reload: step("reload"),
			})
			if (err != nil) != test.fails || (test.fails && !errors.Is(err, cause)) {
				t.Fatalf("uninstall error: %v", err)
			}
			var warning CleanupError
			if got := errors.As(err, &warning); got != (test.fail == "NAT") {
				t.Fatalf("%s reported as a cleanup warning: %v", test.fail, got)
			}
			if !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("calls=%v want=%v", calls, test.want)
			}
		})
	}
}

func TestCancelledUninstallDoesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := executeUninstall(ctx, uninstallActions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled uninstall: %v", err)
	}
}

// A console without dpkg, or with no package entry, owns nothing to delegate.
func TestPackageInstalledWithoutDpkg(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	record, err := QueryPackage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if record.Owned() || record != (PackageRecord{}) {
		t.Fatalf("a console without dpkg reported a package: %+v", record)
	}
}

func TestPackageRecordSpellsTheReleaseVersion(t *testing.T) {
	t.Parallel()
	record := PackageRecord{Status: "installed", Version: "5.0.0~preview.2"}
	if got := record.ReleaseVersion(); got != "5.0.0-preview.2" {
		t.Fatalf("release version = %q", got)
	}
}

// A package dpkg has only unpacked or half configured, as a failed postinst
// leaves it, still belongs to dpkg. A removed package whose configuration
// dpkg remembers does not: a standalone installation made after the removal
// is this program's to take apart.
func TestPackageOwnedCoversPartialStates(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]bool{
		"installed": true, "unpacked": true, "half-configured": true, "half-installed": true,
		"triggers-pending": true, "triggers-awaited": true,
		"config-files": false, "not-installed": false, "": false,
	} {
		if got := packageOwned(status); got != want {
			t.Errorf("packageOwned(%q) = %t, want %t", status, got, want)
		}
	}
}

func TestDelegateRemovalSkipsAnUnownedInstallation(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	commands := packageCommands{
		record: func(context.Context) (PackageRecord, error) { return PackageRecord{}, nil },
		remove: func(context.Context, string, io.Writer, io.Writer) error {
			t.Fatal("called the package manager for an installation dpkg does not track")

			return nil
		},
	}
	delegated, err := delegateRemoval(t.Context(), false, &out, &errOut, commands)
	if err != nil {
		t.Fatal(err)
	}
	if delegated {
		t.Fatal("delegated an installation dpkg does not track")
	}
	if out.Len() != 0 {
		t.Fatalf("announced a removal it did not perform: %q", out.String())
	}
}

// Delegation runs before anything is deleted, so a failed apt-get must not be
// followed by removing the files behind dpkg's back.
func TestDelegateRemovalReportsAptFailure(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	commands := packageCommands{
		record: func(context.Context) (PackageRecord, error) {
			return PackageRecord{Status: "installed", Version: "5.0.0"}, nil
		},
		remove: func(context.Context, string, io.Writer, io.Writer) error {
			return errInjectedUninstallStep
		},
	}
	delegated, err := delegateRemoval(t.Context(), false, &out, &errOut, commands)
	if !errors.Is(err, errInjectedUninstallStep) {
		t.Fatalf("apt-get failure was not reported: %v", err)
	}
	if delegated {
		t.Fatal("reported delegation after the package manager failed")
	}
}

// --keep-config removes; without it the package and its configuration go.
func TestDelegateRemovalChoosesRemoveOrPurge(t *testing.T) {
	t.Parallel()
	for keepConfig, want := range map[bool]string{true: "remove", false: "purge"} {
		var out, errOut bytes.Buffer
		got := ""
		commands := packageCommands{
			record: func(context.Context) (PackageRecord, error) {
				return PackageRecord{Status: "installed", Version: "5.0.0"}, nil
			},
			remove: func(_ context.Context, action string, _, _ io.Writer) error {
				got = action

				return nil
			},
		}
		delegated, err := delegateRemoval(t.Context(), keepConfig, &out, &errOut, commands)
		if err != nil || !delegated {
			t.Fatalf("keepConfig=%t: delegated=%t err=%v", keepConfig, delegated, err)
		}
		if got != want {
			t.Fatalf("keepConfig=%t ran apt-get %s, want %s", keepConfig, got, want)
		}
	}
}
