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

func main() {
	if filepath.Base(os.Args[0]) == "udhcpc-hook" {
		os.Args = append([]string{os.Args[0], "dhcp-hook"}, os.Args[1:]...)
	}
	err := cli.Execute(version)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
