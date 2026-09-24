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

	"github.com/kjanat/udm-iptv/internal/config"
)

var errPreviewAborted = errors.New("cancelled")

type previewCase struct {
	name        string
	promptErr   error
	wantErr     error
	wantPrinted bool
}

func TestPreviewIsIndependentOfHost(t *testing.T) {
	for _, testCase := range []previewCase{
		{name: "completed", wantPrinted: true},
		{name: "cancelled", promptErr: errPreviewAborted, wantErr: errPreviewAborted},
	} {
		t.Run(testCase.name, func(t *testing.T) { runPreviewCase(t, testCase) })
	}
}

func runPreviewCase(t *testing.T, testCase previewCase) {
	t.Helper()
	directory := t.TempDir()
	var output bytes.Buffer
	application := &Application{ConfigPath: filepath.Join(directory, "unreadable-config"), StateDir: directory, Out: &output, Err: &output}
	calls := 0
	command := application.previewCommandWith(func(_ context.Context, value *config.Config) error {
		calls++
		if value.Profile != "tweak" {
			t.Fatal("profile flag not used")
		}
		value.Telemetry.Enabled = true
		value.WAN.VLAN = 123

		return testCase.promptErr
	})
	root := &cobra.Command{Use: "test", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(command)
	root.SetArgs([]string{"preview", "--profile", "tweak"})
	err := root.Execute()
	if !errors.Is(err, testCase.wantErr) {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatal("wizard not called")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("preview wrote files: %v", entries)
	}
	if strings.Contains(output.String(), `"vlan": 123`) != testCase.wantPrinted {
		t.Fatal(output.String())
	}
}
