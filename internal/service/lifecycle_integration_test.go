//go:build linux && integration

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

func systemdTestLifecycle(t *testing.T) (context.Context, Lifecycle) {
	t.Helper()
	// Keep the connection alive during cleanup, after t.Context cancels.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 40*time.Second)
	t.Cleanup(cancel)
	connect := systemd.NewUserConnectionContext
	if os.Geteuid() == 0 {
		connect = systemd.NewSystemConnectionContext
	}
	connection, err := connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Close)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	unit := fmt.Sprintf("udm-iptv-%s-%d.service", t.Name(), os.Getpid())
	path := filepath.Join(t.TempDir(), unit)
	content := "[Unit]\nDescription=IPTV lifecycle test\n[Service]\nType=exec\nExecStart=" + sleep + " 300\n"
	if err := atomicfile.Write(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.LinkUnitFilesContext(ctx, []string{path}, true, false); err != nil {
		t.Fatal(err)
	}
	lifecycle := Lifecycle{Connection: connection, Unit: unit}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := lifecycle.Stop(cleanup); err != nil {
			t.Error(err)
		}
		if _, err := connection.DisableUnitFilesContext(cleanup, []string{unit}, true); err != nil {
			t.Error(err)
		}
		if err := connection.ReloadContext(cleanup); err != nil {
			t.Error(err)
		}
	})
	if err := connection.ReloadContext(ctx); err != nil {
		t.Fatal(err)
	}
	return ctx, lifecycle
}

// TestSystemdLifecycle uses a harmless service in the real systemd manager.
// Non-root runs use the user manager; CI can run it as root in the system manager.
func TestSystemdLifecycle(t *testing.T) {
	ctx, lifecycle := systemdTestLifecycle(t)
	testSystemdRepeatedStart(ctx, t, lifecycle)
	testSystemdResume(ctx, t, lifecycle)
	testSystemdCancelResume(ctx, t, lifecycle)
}

func systemdTestPID(ctx context.Context, t *testing.T, lifecycle Lifecycle) uint32 {
	t.Helper()
	properties, err := lifecycle.Connection.GetUnitTypePropertiesContext(ctx, lifecycle.Unit, "Service")
	if err != nil {
		t.Fatal(err)
	}
	value, _ := properties["MainPID"].(uint32)
	return value
}

func testSystemdRepeatedStart(ctx context.Context, t *testing.T, lifecycle Lifecycle) {
	t.Helper()
	if err := lifecycle.Start(ctx); err != nil {
		t.Fatal(err)
	}
	first := systemdTestPID(ctx, t, lifecycle)
	if first == 0 {
		t.Fatal("service did not start")
	}
	if err := lifecycle.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if systemdTestPID(ctx, t, lifecycle) != first {
		t.Fatal("repeated start replaced the running process")
	}
}

func testSystemdResume(ctx context.Context, t *testing.T, lifecycle Lifecycle) {
	t.Helper()
	if err := lifecycle.Pause(ctx, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if systemdTestPID(ctx, t, lifecycle) != 0 {
		t.Fatal("pause did not stop the process")
	}
	deadline, err := lifecycle.ResumeAt(ctx)
	if err != nil || deadline.Before(time.Now().Add(-time.Second)) || deadline.After(time.Now().Add(3*time.Second)) {
		t.Fatalf("resume deadline=%v err=%v", deadline, err)
	}
	wait := time.NewTicker(50 * time.Millisecond)
	defer wait.Stop()
	for systemdTestPID(ctx, t, lifecycle) == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("timer did not resume service")
		case <-wait.C:
		}
	}
	// A timer that fired once must not start the service again after a plain stop.
	if err := lifecycle.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	assertNoResume(ctx, t, lifecycle)
}

func testSystemdCancelResume(ctx context.Context, t *testing.T, lifecycle Lifecycle) {
	t.Helper()
	for _, operation := range []string{"start", "restart", "uninstall"} {
		if err := lifecycle.Pause(ctx, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		run := map[string]func(context.Context) error{"start": lifecycle.Start, "restart": lifecycle.Restart, "uninstall": lifecycle.Stop}[operation]
		if err := run(ctx); err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		assertNoResume(ctx, t, lifecycle)
		if (systemdTestPID(ctx, t, lifecycle) != 0) != (operation != "uninstall") {
			t.Fatalf("wrong process state after %s", operation)
		}
	}
}

func assertNoResume(ctx context.Context, t *testing.T, lifecycle Lifecycle) {
	t.Helper()
	when, err := lifecycle.ResumeAt(ctx)
	if err != nil || !when.IsZero() {
		t.Fatalf("resume remains: %v (%v)", when, err)
	}
}

func TestSystemdPauseFailureRestoresService(t *testing.T) {
	ctx, lifecycle := systemdTestLifecycle(t)
	conflictResumeTimer(ctx, t, lifecycle)
	if err := lifecycle.Start(ctx); err != nil {
		t.Fatal(err)
	}
	err := lifecycle.Pause(ctx, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "started again") {
		t.Fatalf("pause failure: %v", err)
	}
	if systemdTestPID(ctx, t, lifecycle) == 0 {
		t.Fatal("service not restored")
	}
	assertNoResume(ctx, t, lifecycle)
}

func conflictResumeTimer(ctx context.Context, t *testing.T, lifecycle Lifecycle) {
	t.Helper()
	connection, ok := lifecycle.Connection.(*systemd.Conn)
	if !ok {
		t.Fatal("test requires a real systemd connection")
	}
	// A file-backed timer cannot be replaced by StartTransientUnit. This forces
	// systemd itself to reject scheduling after the real service has stopped.
	path := filepath.Join(t.TempDir(), lifecycle.timer())
	content := "[Unit]\nDescription=Conflicting test timer\n[Timer]\nOnActiveSec=1h\n"
	if err := atomicfile.Write(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.LinkUnitFilesContext(ctx, []string{path}, true, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := connection.DisableUnitFilesContext(ctx, []string{lifecycle.timer()}, true); err != nil {
			t.Error(err)
		}
		if err := connection.ReloadContext(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := connection.ReloadContext(ctx); err != nil {
		t.Fatal(err)
	}
}
