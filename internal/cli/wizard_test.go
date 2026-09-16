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

func TestPreviewIsIndependentOfHost(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		directory := t.TempDir()
		var output bytes.Buffer
		application := &Application{ConfigPath: filepath.Join(directory, "unreadable-config"), StateDir: directory, Out: &output, Err: &output}
		calls := 0
		abort := errors.New("cancelled")
		command := application.previewCommandWith(func(_ context.Context, value *config.Config) error {
			calls++
			if value.Profile != "tweak" {
				t.Fatal("profile flag not used")
			}
			value.Telemetry.Enabled = true
			value.WAN.VLAN = 123
			if cancelled {
				return abort
			}

			return nil
		})
		root := &cobra.Command{Use: "test", SilenceErrors: true, SilenceUsage: true}
		root.AddCommand(command)
		application.instrumentCommands(root)
		root.SetArgs([]string{"preview", "--profile", "tweak"})
		err := root.Execute()
		if cancelled && !errors.Is(err, abort) || !cancelled && err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 1 {
			t.Fatal("wizard not called")
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("preview wrote files: %v", err)
		}
		printed := strings.Contains(output.String(), `"vlan": 123`)
		if cancelled && printed {
			t.Fatal("cancelled preview printed configuration")
		}
		if !cancelled && !printed {
			t.Fatal(output.String())
		}
	}
}
