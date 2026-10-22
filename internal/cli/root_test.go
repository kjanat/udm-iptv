package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestExecuteRootCancelsOnTermination(t *testing.T) {
	if os.Getenv("UDM_IPTV_TEST_ROOT_SIGNAL") == "1" {
		command := &cobra.Command{Use: "signal-test", RunE: func(command *cobra.Command, _ []string) error {
			if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
				return fmt.Errorf("signal test handshake: %w", err)
			}
			<-command.Context().Done()
			return command.Context().Err()
		}}
		command.SetArgs([]string{})
		if err := executeRoot(command); !errors.Is(err, context.Canceled) {
			t.Fatalf("signal did not cancel command: %v", err)
		}
		return
	}
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecuteRootCancelsOnTermination$")
	command.Env = append(os.Environ(), "UDM_IPTV_TEST_ROOT_SIGNAL=1")
	var errorOutput bytes.Buffer
	command.Stderr = &errorOutput
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	ready, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || ready != "ready\n" {
		cancel()
		waitErr := command.Wait()
		t.Fatalf("signal handshake %q: %v; wait=%v; %s", ready, err, waitErr, errorOutput.String())
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Error(err)
		cancel()
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("signal-aware root: %v; %s", err, errorOutput.String())
	}
}
