package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/device"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

const (
	commandConfigure = "configure"
	commandInstall   = "install"
)

func setTelemetryMetadata(ctx context.Context, reporter *telemetry.Reporter, value config.Config) {
	hw := device.Inspect(ctx)
	reporter.SetMetadata(hw.Board, hw.Firmware, hw.Discovery, hw.SysID, value.Proxy.Program, value.Profile)
}

// cobraRun is a Cobra RunE handler.
type cobraRun func(*cobra.Command, []string) error

// reporting wraps run so the invocation is reported as operation whenever the
// saved configuration enables reporting. A command opts in by wrapping its own
// handler, so nothing rewrites the command tree after it is built.
func (application *Application) reporting(operation string, run cobraRun) cobraRun {
	return func(command *cobra.Command, args []string) error {
		value, err := config.Load(application.ConfigPath)
		if err != nil || !value.Telemetry.Enabled {
			return run(command, args)
		}
		reporter, err := telemetry.New(value.Telemetry, application.Version, application.ConfigPath, application.StateDir)
		if err != nil {
			return run(command, args)
		}
		defer reporter.Close()
		setTelemetryMetadata(command.Context(), reporter, value)
		application.monitor = reporter
		defer func() { application.monitor = nil }()

		return reporter.Run(command.Context(), operation, func(ctx context.Context) error {
			command.SetContext(ctx)

			return run(command, args)
		})
	}
}

// reportingSaved wraps a command that persists a configuration. bypass skips
// reporting entirely for an invocation; otherwise whatever the command saved
// is reported once it returns, which covers both first-install consent and an
// opt-out chosen inside the wizard.
func (application *Application) reportingSaved(operation string, bypass func(*cobra.Command) bool, run cobraRun) cobraRun {
	return func(command *cobra.Command, args []string) error {
		if bypass(command) {
			return run(command, args)
		}
		application.reportConfig, application.reportApplied = nil, false
		defer func() { application.reportSavedConfiguration(command) }()

		return application.reporting(operation, run)(command, args)
	}
}

// reportingHook wraps the DHCP hook, whose argument names the event handled.
func (application *Application) reportingHook(run cobraRun) cobraRun {
	return func(command *cobra.Command, args []string) error {
		operation := command.Name()
		if len(args) == 1 {
			operation = "dhcp." + args[0]
		}

		return application.reporting(operation, run)(command, args)
	}
}

// reportingTurnedOff reports that this invocation switches reporting off, so
// it must not open a reporter of its own.
func reportingTurnedOff(command *cobra.Command) bool {
	if !command.Flags().Changed("telemetry") {
		return false
	}
	enabled, _ := command.Flags().GetBool("telemetry")

	return !enabled
}

// previewOnly reports that this invocation changes nothing worth reporting.
func previewOnly(command *cobra.Command) bool {
	dryRun, _ := command.Flags().GetBool("dry-run")

	return dryRun
}

func (application *Application) reportRun(ctx context.Context, operation string, run func(context.Context) error) error {
	if application.monitor != nil {
		if err := application.monitor.Run(ctx, operation, run); err != nil {
			return fmt.Errorf("%s: %w", operation, err)
		}

		return nil
	}
	value, err := config.Load(application.ConfigPath)
	if err != nil || !value.Telemetry.Enabled {
		return run(ctx)
	}
	reporter, err := telemetry.New(value.Telemetry, application.Version, application.ConfigPath, application.StateDir)
	if err != nil {
		return run(ctx)
	}
	defer reporter.Close()
	setTelemetryMetadata(ctx, reporter, value)
	if err := reporter.Run(ctx, operation, run); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}

	return nil
}
