package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

// reportConfigTimeout bounds how long the best-effort configuration report waits.
const reportConfigTimeout = 3 * time.Second

func (application *Application) reportSavedConfiguration(command *cobra.Command) {
	if application.reportConfig == nil {
		return
	}
	// Read the saved choice again, including first-install consent and opt-out
	// selected inside the wizard. The old invocation reporter may be closed now.
	value, err := config.Load(application.ConfigPath)
	if err != nil || !value.Telemetry.Enabled || !value.Telemetry.Presets {
		return
	}
	reporter, err := telemetry.New(value.Telemetry, application.Version, application.ConfigPath, application.StateDir)
	if err != nil {
		return
	}
	defer reporter.Close()
	setTelemetryMetadata(reporter, value)
	ctx, cancel := context.WithTimeout(command.Context(), reportConfigTimeout)
	defer cancel()
	_ = reporter.RecordConfiguration(ctx, *application.reportConfig, application.reportApplied, application.networkIdentity)
}

func (application *Application) telemetryCommand() *cobra.Command {
	command := &cobra.Command{Use: "telemetry", Hidden: true, Short: "Manage reporting identity and provide IPTV feedback"}
	command.AddCommand(&cobra.Command{
		Use: "reset-id", Short: "Reset local reporting identity; previously sent reports remain", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			err := telemetry.ResetIdentity(application.StateDir)
			if err != nil {
				return fmt.Errorf("reset the reporting identity in %s: %w", application.StateDir, err)
			}

			return writeString(application.Out, "Reporting identity reset. Previously sent reports are unchanged.\n")
		},
	})
	var provider string
	feedback := &cobra.Command{
		Use: "feedback working|problems|not-using", Short: "Report your experience explicitly (requires preset research)", Args: cobra.ExactArgs(1),
		ValidArgs: []string{"working", "problems", "not-using"},
		RunE: func(_ *cobra.Command, args []string) error {
			value, err := config.Load(application.ConfigPath)
			if err != nil {
				return fmt.Errorf("load configuration from %s: %w", application.ConfigPath, err)
			}
			reporter, err := telemetry.New(value.Telemetry, application.Version, application.ConfigPath, application.StateDir)
			if err != nil {
				return fmt.Errorf("open the reporter: %w", err)
			}
			defer reporter.Close()
			if err := reporter.Feedback(args[0], provider); err != nil {
				return fmt.Errorf("queue the feedback: %w", err)
			}

			return writeString(application.Out, "Feedback submitted to the reporting queue; delivery is best effort.\n")
		},
	}
	feedback.Flags().StringVar(&provider, "provider", "", "confirm your provider by provider or profile ID")
	command.AddCommand(feedback)

	return command
}
