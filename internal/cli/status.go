package cli

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/diagnostics"
)

func (application *Application) statusCommand() *cobra.Command {
	var outputJSON bool
	command := &cobra.Command{
		Use: "status", Short: "Show the current IPTV state", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			value, err := application.collector().Snapshot(command.Context())
			if err != nil {
				return err
			}
			if outputJSON {
				encoder := json.NewEncoder(application.Out)
				encoder.SetIndent("", "  ")

				return encoder.Encode(value)
			}

			return writeString(application.Out, diagnostics.RenderSnapshot(value))
		},
	}
	command.Flags().BoolVar(&outputJSON, "json", false, "write structured JSON")

	return command
}

func (application *Application) collector() *diagnostics.Collector {
	return &diagnostics.Collector{ConfigPath: application.ConfigPath, Version: application.Version}
}
