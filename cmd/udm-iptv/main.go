package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kjanat/udm-iptv/internal/app"
)

var version = "dev"

func main() {
	if filepath.Base(os.Args[0]) == "udhcpc-hook" {
		os.Args = append([]string{os.Args[0], "dhcp-hook"}, os.Args[1:]...)
	}
	if err := app.Execute(version); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
