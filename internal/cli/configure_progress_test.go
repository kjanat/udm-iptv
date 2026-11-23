package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

func TestConfigurationProgressFollowsCompletedSteps(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"success", "not-installed", "restart-failure", "health-failure"} {
		t.Run(stage, func(t *testing.T) {
			testConfigurationProgress(t, stage)
		})
	}
}

func testConfigurationProgress(t *testing.T, stage string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	previous, attempted := []byte("previous"), []byte("attempted")
	if err := atomicfile.Write(path, attempted, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	application := &Application{ConfigPath: path, Out: &output}
	saved := "Configuration saved to " + path + ".\n"
	restarts, checks := 0, 0
	restart := func(ctx context.Context, verify bool) error {
		restarts++
		if restarts > 1 {
			assertRecoveredConfiguration(ctx, t, verify, path, previous)
			return nil
		}
		if verify || output.String() != saved {
			t.Fatalf("restart sees premature or missing progress: verify=%t output=%q", verify, output.String())
		}
		if stage == "restart-failure" {
			return errTestActivation
		}
		return nil
	}
	check := func(context.Context) error {
		checks++
		if got, want := output.String(), saved+"Service restarted.\n"; got != want {
			t.Fatalf("health check sees premature or missing progress: got %q, want %q", got, want)
		}
		if stage == "health-failure" {
			return errTestActivation
		}
		return nil
	}
	err := application.finishConfiguration(t.Context(), stage != "not-installed", previous, restart, check)
	assertConfigurationProgress(t, stage, application.reportApplied, err, output.String(), restarts, checks)
}

func assertConfigurationProgress(t *testing.T, stage string, applied bool, err error, output string, restarts, checks int) {
	t.Helper()
	failed := strings.HasSuffix(stage, "failure")
	if errors.Is(err, errTestActivation) != failed {
		t.Fatalf("stage %s: %v", stage, err)
	}
	if !failed && err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int{"success": {1, 1}, "not-installed": {0, 0}, "restart-failure": {2, 0}, "health-failure": {2, 1}}[stage]
	if got := [2]int{restarts, checks}; got != want {
		t.Fatalf("restarts/checks=%v, want %v", got, want)
	}
	if got := strings.Contains(output, "Health checks passed."); got != (stage == "success") {
		t.Fatalf("false or missing health success: %q", output)
	}
	if stage == "not-installed" && strings.Contains(output, "Service restarted.") {
		t.Fatalf("uninstalled service reported restarted: %q", output)
	}
	if applied != (stage == "success") {
		t.Fatalf("applied=%t for %s", applied, stage)
	}
}

// Output failure after any step must neither skip later work nor roll back a
// successfully applied configuration. It must not hide an operational failure.
func TestConfigurationProgressOutputFailuresDoNotDriveRollback(t *testing.T) {
	t.Parallel()
	for _, failAt := range []int{1, 2, 3} {
		for _, healthFails := range []bool{false, true} {
			testConfigurationOutputFailure(t, failAt, healthFails)
		}
	}
}

func testConfigurationOutputFailure(t *testing.T, failAt int, healthFails bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	previous, attempted := []byte("previous"), []byte("attempted")
	if err := atomicfile.Write(path, attempted, filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	output := &progressWriter{failAt: failAt}
	application := &Application{Out: output, ConfigPath: path}
	restarts, checks := 0, 0
	err := application.finishConfiguration(t.Context(), true, previous, func(ctx context.Context, verify bool) error {
		restarts++
		if restarts > 1 {
			assertRecoveredConfiguration(ctx, t, verify, path, previous)
		}
		return nil
	}, func(context.Context) error {
		checks++
		if healthFails {
			return errTestActivation
		}
		return nil
	})
	wantRestarts := 1
	wantFile := attempted
	if healthFails {
		wantRestarts, wantFile = 2, previous
	}
	if !errors.Is(err, io.ErrClosedPipe) || errors.Is(err, errTestActivation) != healthFails || restarts != wantRestarts || checks != 1 {
		t.Fatalf("write=%d healthFailure=%t: err=%v restarts=%d checks=%d", failAt, healthFails, err, restarts, checks)
	}
	assertProgressConfigurationFile(t, path, wantFile)
}

func assertProgressConfigurationFile(t *testing.T, path string, wantFile []byte) {
	t.Helper()
	data, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(data, wantFile) {
		t.Fatalf("output failure changed recovery decision: %q, %v", data, readErr)
	}
}

type progressWriter struct {
	writes int
	failAt int
}

func (writer *progressWriter) Write(data []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.failAt {
		return 0, io.ErrClosedPipe
	}
	return len(data), nil
}
