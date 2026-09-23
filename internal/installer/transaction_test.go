package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

func TestInstallationSnapshotRestoresFilesLinksAndRuntime(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	paths := []string{filepath.Join(directory, "binary"), filepath.Join(directory, "config"), filepath.Join(directory, "unit"), filepath.Join(directory, "runtime"), filepath.Join(directory, "new-link")}
	for _, path := range paths[:3] {
		writeRecoveryFixture(t, path, "previous", filemode.PrivateFile)
	}
	writeRecoveryFixture(t, filepath.Join(paths[3], "old-generation", "program"), "previous runtime", filemode.Executable)
	if err := os.Symlink("old-generation", filepath.Join(paths[3], "improxy")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotInstallation(directory, paths)
	if err != nil {
		t.Fatal(err)
	}
	replaceRecoveryFixtures(t, paths)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	recovered := false
	err = snapshot.finish(ctx, errInjectedPlanStep, func(ctx context.Context) error {
		assertRecoveryContext(ctx, t)
		return nil
	}, func(ctx context.Context) error {
		assertRecoveryContext(ctx, t)
		for _, path := range paths[:3] {
			assertRecoveryFixture(t, path, "previous", filemode.PrivateFile)
		}
		assertRecoveryFixture(t, filepath.Join(paths[3], "improxy", "program"), "previous runtime", filemode.Executable)
		if _, err := os.Lstat(paths[4]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("new installation artifact survived: %v", err)
		}
		recovered = true
		return nil
	})
	if !errors.Is(err, errInjectedPlanStep) || !recovered {
		t.Fatalf("recovered=%t err=%v", recovered, err)
	}
	if _, err := os.Stat(snapshot.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful recovery left temporary backup: %v", err)
	}
}

func replaceRecoveryFixtures(t *testing.T, paths []string) {
	t.Helper()
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		writeRecoveryFixture(t, path, "rejected", filemode.SharedFile)
	}
}

func writeRecoveryFixture(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := atomicfile.Write(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func assertRecoveryFixture(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != contents {
		t.Fatalf("restored %s: %q, %v", path, data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("restored %s mode: %v, %v", path, info, err)
	}
}

func assertRecoveryContext(ctx context.Context, t *testing.T) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if ctx.Err() != nil || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Minute {
		t.Fatalf("invalid recovery context: error=%v deadline=%v", ctx.Err(), deadline)
	}
}

func TestFailedRecoveryKeepsSnapshot(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "binary")
	writeRecoveryFixture(t, path, "previous", filemode.Executable)
	snapshot, err := snapshotInstallation(directory, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	err = snapshot.finish(t.Context(), errInjectedPlanStep, func(context.Context) error {
		return errInjectedUninstallStep
	}, func(context.Context) error {
		t.Fatal("restarted after failed stop")
		return nil
	})
	if !errors.Is(err, errInjectedPlanStep) || !errors.Is(err, errInjectedUninstallStep) {
		t.Fatalf("lost failure causes: %v", err)
	}
	assertRecoveryFixture(t, snapshot.entry(0), "previous", filemode.Executable)
}

func TestSuccessfulReplacementDiscardsSnapshotWithoutRecovery(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "binary")
	writeRecoveryFixture(t, path, "previous", filemode.Executable)
	snapshot, err := snapshotInstallation(directory, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	writeRecoveryFixture(t, path, "replacement", filemode.Executable)
	unexpected := func(context.Context) error {
		t.Fatal("successful installation invoked recovery")
		return nil
	}
	if err := snapshot.finish(t.Context(), nil, unexpected, unexpected); err != nil {
		t.Fatal(err)
	}
	assertRecoveryFixture(t, path, "replacement", filemode.Executable)
	if _, err := os.Stat(snapshot.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful replacement retained backup: %v", err)
	}
}

func TestInstallationSnapshotRejectsUnexpectedDirectory(t *testing.T) {
	t.Parallel()
	directory, unrelated := t.TempDir(), t.TempDir()
	path := filepath.Join(unrelated, "notes")
	writeRecoveryFixture(t, path, "unrelated", filemode.PrivateFile)
	if _, err := snapshotInstallation(directory, []string{unrelated}); !errors.Is(err, errStateFileIsDirectory) {
		t.Fatalf("accepted unrelated directory as a managed file: %v", err)
	}
	assertRecoveryFixture(t, path, "unrelated", filemode.PrivateFile)
}

type transactionalTestBackend struct {
	recordingBackend

	finished bool
	cause    error
}

func (backend *transactionalTestBackend) Begin(context.Context, Plan) (InstallationTransaction, error) {
	return InstallationTransaction{finish: func(_ context.Context, cause error) error {
		backend.finished, backend.cause = true, cause
		return cause
	}}, nil
}

func TestInstallationFinalizesTransactionAtFailureBoundaries(t *testing.T) {
	t.Parallel()
	plan := testPlan()
	for _, failure := range []string{"", "Install persistent executable", "Reload systemd, enable and restart service", "Wait for stable proxy readiness"} {
		backend := &transactionalTestBackend{fail: failure}
		err := plan.Execute(t.Context(), backend)
		if !backend.finished || (failure != "") != errors.Is(err, errInjectedPlanStep) || !errors.Is(backend.cause, err) {
			t.Fatalf("failure=%q finished=%t cause=%v error=%v", failure, backend.finished, backend.cause, err)
		}
	}
}
