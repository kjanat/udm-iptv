package telemetry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

func TestPersistedBudgetReasons(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	minute, day := now.Unix()/secondsPerMinute, now.Unix()/secondsPerDay
	for _, test := range []struct {
		name, content string
		want          error
	}{
		{"minute", fmt.Sprintf("%d 5 %d 5", minute, day), errRateMinute},
		{"day", fmt.Sprintf("%d 0 %d 600", minute, day), errRateDay},
		{"clock", fmt.Sprintf("%d 0 %d 0", minute+1, day), errRateClock},
		{"negative", fmt.Sprintf("%d -1 %d 0", minute, day), errRateRecord},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "telemetry-errors.rate")
			if err := atomicfile.Write(path, []byte(test.content), filemode.PrivateFile); err != nil {
				t.Fatal(err)
			}
			if err := allowPersistedReason(dir, "errors", 5, now); !errors.Is(err, test.want) {
				t.Fatalf("budget rejection = %v, want %v", err, test.want)
			}
			content, err := os.ReadFile(path)
			if err != nil || string(content) != test.content {
				t.Fatalf("rejection altered budget: %q, %v", content, err)
			}
		})
	}
}

func TestPersistedBudgetStorageReasons(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, want string }{
		{"directory", "create telemetry budget directory"},
		{"open", "open telemetry budget"},
		{"lock", "lock telemetry budget"},
		{"corrupt", "read telemetry budget"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := brokenBudgetStorage(t, test.name)
			if err := allowPersistedReason(dir, "errors", 5, time.Now()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("storage rejection = %v, want %s", err, test.want)
			}
		})
	}
}

func brokenBudgetStorage(t *testing.T, fault string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry-errors.rate")
	switch fault {
	case "directory":
		if err := atomicfile.Write(path, nil, filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
		dir = filepath.Join(path, "child")
	case "open":
		if err := os.Mkdir(path, filemode.PrivateDir); err != nil {
			t.Fatal(err)
		}
	case "lock":
		budget, err := openRateBudget(dir, "errors")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(budget.release)
	case "corrupt":
		if err := atomicfile.Write(path, []byte("broken budget"), filemode.PrivateFile); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
