package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

var (
	errInjectedStep        = errors.New("injected")
	errInjectedHealthCheck = errors.New("health check failed")
)

type upgradeRecorder struct {
	t        *testing.T
	calls    []string
	files    map[string]string
	failures map[string]error
	restarts int
}

func newUpgradeRecorder(t *testing.T, fail []string) *upgradeRecorder {
	t.Helper()
	recorder := &upgradeRecorder{t: t, files: map[string]string{"source": "new", "target": "old"}, failures: make(map[string]error)}
	for _, name := range fail {
		recorder.failures[name] = fmt.Errorf("%w %s", errInjectedStep, name)
	}

	return recorder
}

func (r *upgradeRecorder) step(name string) error {
	r.calls = append(r.calls, name)

	return r.failures[name]
}

func (r *upgradeRecorder) backup(target string) (string, error) {
	if err := r.step("backup"); err != nil {
		return "", err
	}
	r.files["backup"] = r.files[target]

	return "backup", nil
}

func (r *upgradeRecorder) copy(source, target string) error {
	name := "install"
	if source == "backup" {
		name = "restore"
	}
	if err := r.step(name); err != nil {
		return err
	}
	r.files[target] = r.files[source]

	return nil
}

func (r *upgradeRecorder) restart(_ context.Context, healthy bool) error {
	if !healthy {
		r.t.Fatal("restart omitted health verification")
	}
	r.restarts++
	if r.restarts == 1 {
		return r.step("activate")
	}

	return r.step("recover")
}

func (r *upgradeRecorder) remove(name string) error {
	if err := r.step("cleanup"); err != nil {
		return err
	}
	delete(r.files, name)

	return nil
}

func (r *upgradeRecorder) actions() upgradeActions {
	return upgradeActions{backup: r.backup, copy: r.copy, restart: r.restart, remove: r.remove}
}

func (r *upgradeRecorder) assertCauses(err error) {
	r.t.Helper()
	for _, cause := range r.failures {
		if !errors.Is(err, cause) {
			r.t.Fatalf("lost cause %v: %v", cause, err)
		}
	}
	if len(r.failures) == 0 && err != nil {
		r.t.Fatal(err)
	}
}

func TestUpgradeFailureBoundaries(t *testing.T) {
	for _, test := range []struct {
		name   string
		fail   []string
		want   []string
		backup bool
		target string
	}{
		{"success", nil, []string{"backup", "install", "activate", "cleanup"}, false, "new"},
		{"backup", []string{"backup"}, []string{"backup"}, false, "old"},
		{"install", []string{"install"}, []string{"backup", "install"}, true, "old"},
		{"rollback", []string{"activate"}, []string{"backup", "install", "activate", "restore", "recover", "cleanup"}, false, "old"},
		{"restore", []string{"activate", "restore"}, []string{"backup", "install", "activate", "restore"}, true, "new"},
		{"recover", []string{"activate", "recover"}, []string{"backup", "install", "activate", "restore", "recover"}, true, "old"},
		{"cleanup", []string{"cleanup"}, []string{"backup", "install", "activate", "cleanup"}, true, "new"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newUpgradeRecorder(t, test.fail)
			err := activateUpgrade(t.Context(), "source", "target", "next", recorder.actions())
			recorder.assertCauses(err)
			if !reflect.DeepEqual(recorder.calls, test.want) || recorder.files["target"] != test.target {
				t.Fatalf("calls=%v files=%v", recorder.calls, recorder.files)
			}
			_, retained := recorder.files["backup"]
			if retained != test.backup {
				t.Fatalf("backup retained=%t, want %t", retained, test.backup)
			}
			if retained && (err == nil || !strings.Contains(err.Error(), "backup")) {
				t.Fatalf("missing recovery location: %v", err)
			}
		})
	}
}

func TestRollbackSurvivesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	restarts := 0
	actions := upgradeActions{
		backup: func(string) (string, error) { return "backup", nil },
		copy:   func(string, string) error { return nil },
		remove: func(string) error { return nil },
		restart: func(ctx context.Context, _ bool) error {
			restarts++
			if restarts == 1 {
				cancel()
				return ctx.Err()
			}
			deadline, ok := ctx.Deadline()
			if ctx.Err() != nil || !ok || time.Until(deadline) > time.Minute || time.Until(deadline) <= 0 {
				t.Fatal("rollback did not receive an independent bounded context")
			}
			return nil
		},
	}
	if err := activateUpgrade(ctx, "source", "target", "next", actions); !errors.Is(err, context.Canceled) || restarts != 2 {
		t.Fatalf("rollback: restarts=%d error=%v", restarts, err)
	}
	if err := activateUpgrade(ctx, "source", "target", "next", upgradeActions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled upgrade: %v", err)
	}
}

func TestBackupExecutablePreservesExistingRecoveryCopies(t *testing.T) {
	target := filepath.Join(t.TempDir(), "udm-iptv")
	if err := atomicfile.Write(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	first, err := backupExecutable(target)
	if err != nil {
		t.Fatal(err)
	}
	second, err := backupExecutable(target)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("overwrote existing recovery copy")
	}
	for _, name := range []string{first, second} {
		data, err := os.ReadFile(name)
		if err != nil || string(data) != "old binary" {
			t.Fatalf("recovery copy %s: %v", name, err)
		}
	}
}

func TestCancelledUpgradeRetainsCompletedBackup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := activateUpgrade(ctx, "source", "target", "next", upgradeActions{
		backup: func(string) (string, error) {
			cancel()
			return "recovery-copy", nil
		},
	})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "recovery-copy") {
		t.Fatalf("missing retained backup after cancellation: %v", err)
	}
}

func upgradeFixture(t *testing.T) (string, string, string) {
	t.Helper()
	directory := t.TempDir()
	source, target := filepath.Join(directory, "download"), filepath.Join(directory, "udm-iptv")
	for name, content := range map[string]string{source: "new", target: "old"} {
		if err := atomicfile.Write(name, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	return directory, source, target
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("%s holds %q, want %q: %v", path, data, want, err)
	}
}

func TestActivationWithFilesystem(t *testing.T) {
	for _, test := range []struct {
		name                    string
		failFirst, failRecovery bool
		fails                   bool
		installed               string
		backups                 int
	}{
		{"healthy", false, false, false, "new", 0},
		{"rollback", true, false, true, "old", 0},
		{"recovery-failure", true, true, true, "old", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory, source, target := upgradeFixture(t)
			restarts := 0
			actions := systemUpgradeActions(func(context.Context, bool) error {
				restarts++
				if restarts == 1 && test.failFirst || restarts > 1 && test.failRecovery {
					return errInjectedHealthCheck
				}

				return nil
			})
			err := activateUpgrade(t.Context(), source, target, "next", actions)
			if (err != nil) != test.fails {
				t.Fatalf("activation outcome: %v", err)
			}
			assertContent(t, target, test.installed)
			backups, err := filepath.Glob(filepath.Join(directory, ".udm-iptv.previous-*"))
			if err != nil || len(backups) != test.backups {
				t.Fatalf("recovery copies: %v: %v", backups, err)
			}
			for _, backup := range backups {
				assertContent(t, backup, "old")
			}
		})
	}
}
