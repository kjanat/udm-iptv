// Command udm-iptv routes provider IPTV traffic over a UniFi OS gateway.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kjanat/udm-iptv/internal/cli"
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
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			os.Exit(sigintExitCode)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if errors.Is(err, cli.ErrNotConfigured) {
			os.Exit(notConfiguredExitCode)
		}
		os.Exit(1)
	}
}
