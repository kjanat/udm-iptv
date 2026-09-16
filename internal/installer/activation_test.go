package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

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
			files := map[string]string{"source": "new", "target": "old"}
			var calls []string
			failures := make(map[string]error)
			for _, name := range test.fail {
				failures[name] = errors.New("injected " + name)
			}
			step := func(name string) error {
				calls = append(calls, name)
				return failures[name]
			}
			restarts := 0
			actions := upgradeActions{
				backup: func(target string) (string, error) {
					if err := step("backup"); err != nil {
						return "", err
					}
					files["backup"] = files[target]
					return "backup", nil
				},
				copy: func(source, target string) error {
					name := "install"
					if source == "backup" {
						name = "restore"
					}
					if err := step(name); err != nil {
						return err
					}
					files[target] = files[source]
					return nil
				},
				restart: func(_ context.Context, healthy bool) error {
					if !healthy {
						t.Fatal("restart omitted health verification")
					}
					restarts++
					if restarts == 1 {
						return step("activate")
					}
					return step("recover")
				},
				remove: func(name string) error {
					if err := step("cleanup"); err != nil {
						return err
					}
					delete(files, name)
					return nil
				},
			}
			err := activateUpgrade(t.Context(), "source", "target", "next", actions)
			for _, cause := range failures {
				if !errors.Is(err, cause) {
					t.Fatalf("lost cause %v: %v", cause, err)
				}
			}
			if len(failures) == 0 && err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, test.want) || files["target"] != test.target {
				t.Fatalf("calls=%v files=%v", calls, files)
			}
			_, retained := files["backup"]
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

func TestActivationWithFilesystem(t *testing.T) {
	for _, outcome := range []string{"healthy", "rollback", "recovery-failure"} {
		t.Run(outcome, func(t *testing.T) {
			directory := t.TempDir()
			source, target := filepath.Join(directory, "download"), filepath.Join(directory, "udm-iptv")
			for name, content := range map[string]string{source: "new", target: "old"} {
				if err := atomicfile.Write(name, []byte(content), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			restarts := 0
			actions := systemUpgradeActions(func(context.Context, bool) error {
				restarts++
				if outcome == "recovery-failure" || (outcome == "rollback" && restarts == 1) {
					return errors.New("health check failed")
				}
				return nil
			})
			err := activateUpgrade(t.Context(), source, target, "next", actions)
			if (err == nil) != (outcome == "healthy") {
				t.Fatalf("activation outcome: %v", err)
			}
			want := "old"
			if outcome == "healthy" {
				want = "new"
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != want {
				t.Fatalf("installed content: %q: %v", data, err)
			}
			backups, err := filepath.Glob(filepath.Join(directory, ".udm-iptv.previous-*"))
			wantBackups := 0
			if outcome == "recovery-failure" {
				wantBackups = 1
			}
			if err != nil || len(backups) != wantBackups {
				t.Fatalf("recovery copies: %v: %v", backups, err)
			}
			for _, backup := range backups {
				data, err := os.ReadFile(backup)
				if err != nil || string(data) != "old" {
					t.Fatalf("retained backup: %q: %v", data, err)
				}
			}
		})
	}
}
