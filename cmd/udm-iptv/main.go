// Command udm-iptv routes provider IPTV traffic over a UniFi OS gateway.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kjanat/udm-iptv/internal/cli"
	"github.com/kjanat/udm-iptv/internal/ui"
)

var version = "dev"

const (
	// notConfiguredExitCode lets a maintainer script tell an unconfigured console from a failure.
	notConfiguredExitCode = 3
	// sigintExitCode follows the POSIX convention of 128+signal for SIGINT.
	sigintExitCode = 130
)

func main() {
	if filepath.Base(os.Args[0]) == "udhcpc-hook" {
		os.Args = append([]string{os.Args[0], "dhcp-hook"}, os.Args[1:]...)
	}
	err := cli.Execute(version)
	if err != nil {
		stderr := ui.Styled(os.Stderr)
		if errors.Is(err, context.Canceled) {
			_, _ = fmt.Fprintln(stderr, "Cancelled.")
			os.Exit(sigintExitCode)
		}
		_, _ = fmt.Fprintln(stderr, ui.ErrorText(fmt.Sprintf("error: %v", err)))
		if errors.Is(err, cli.ErrNotConfigured) {
			os.Exit(notConfiguredExitCode)
		}
		os.Exit(1)
	}
}
