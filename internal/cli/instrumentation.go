package cli

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/kjanat/udm-iptv/internal/device"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/telemetry"
	"github.com/spf13/cobra"
)

func setTelemetryMetadata(reporter *telemetry.Reporter, value config.Config) {
	firmware := ""
	if file, err := os.Open("/usr/lib/version"); err == nil {
		data, _ := io.ReadAll(io.LimitReader(file, 64))
		_ = file.Close()
		firmware = strings.TrimSpace(string(data))
	}
	reporter.SetMetadata(device.Board(), firmware, value.Proxy.Program, value.Profile)
}

func (application *Application) instrumentCommands(root *cobra.Command) {
	for _, command := range root.Commands() {
		application.instrumentCommands(command)
		switch command.Name() {
		case "configure", "install", "upgrade", "restart", "uninstall", "daemon", "dhcp-hook":
		default:
			continue
		}
		run := command.RunE
		if run == nil {
			continue
		}
		command.RunE = func(command *cobra.Command, args []string) error {
			if command.Name() == "configure" && command.Flags().Changed("telemetry") {
				if enabled, _ := command.Flags().GetBool("telemetry"); !enabled {
					return run(command, args)
				}
			}
			if command.Name() == "install" {
				if dryRun, _ := command.Flags().GetBool("dry-run"); dryRun {
					return run(command, args)
				}
			}
			// Reporting after the command also covers first installation and a
			// previously disabled user enabling reporting in the wizard.
			if command.Name() == "configure" || command.Name() == "install" {
				application.reportConfig, application.reportApplied = nil, false
				defer func() { application.reportSavedConfiguration(command) }()
			}
			value, err := config.Load(application.ConfigPath)
			if err != nil || !value.Telemetry.Enabled {
				return run(command, args)
			}
			reporter, err := telemetry.New(value.Telemetry, application.Version, application.ConfigPath, application.StateDir)
			if err != nil {
				return run(command, args)
			}
			defer reporter.Close()
			setTelemetryMetadata(reporter, value)
			application.monitor = reporter
			defer func() { application.monitor = nil }()
			operation := command.Name()
			if operation == "dhcp-hook" && len(args) == 1 {
				operation = "dhcp." + args[0]
			}

			return reporter.Run(command.Context(), operation, func(ctx context.Context) error {
				command.SetContext(ctx)

				return run(command, args)
			})
		}
	}
}
