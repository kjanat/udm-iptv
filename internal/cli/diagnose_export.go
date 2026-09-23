package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/diagnostics"
)

func (application *Application) diagnoseExportCommand() *cobra.Command {
	format := formatJSONL
	command := &cobra.Command{
		Use:   "export CAPTURE.jsonl",
		Short: "Export a sanitized copy of a private capture",
		Long:  "Export aliases locally; omit free text and DHCP option payloads.",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if format != formatText && format != formatJSONL {
				return fmt.Errorf("%w %q: export supports text or jsonl", errUnknownFormat, format)
			}
			input, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open private capture: %w", err)
			}
			defer closeIgnoringError(input)
			return diagnostics.ExportCapture(input, application.Out, format)
		},
	}
	command.Flags().StringVar(&format, "format", formatJSONL, "sanitized export format: text or jsonl")
	return command
}
