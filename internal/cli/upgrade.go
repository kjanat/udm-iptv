package cli

import (
	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/installer"
)

func (application *Application) upgradeCommand() *cobra.Command {
	options := installer.UpgradeOptions{Repository: "kjanat/udm-iptv"}
	command := &cobra.Command{
		Use: "upgrade", Short: "Install the latest udm-iptv release", Args: cobra.NoArgs,
		RunE: application.reporting("upgrade", func(command *cobra.Command, _ []string) error {
			err := requireRoot()
			if err != nil {
				return err
			}

			return (&installer.Upgrader{Version: application.Version, StateDir: application.StateDir, Out: application.Out, Restart: application.restart}).Upgrade(command.Context(), options)
		}),
	}
	flags := command.Flags()
	flags.StringVar(&options.Repository, "repository", options.Repository, "GitHub repository")
	flags.StringVar(&options.Version, "version", "latest", "release version or latest")
	flags.StringVar(&options.TokenFile, "token-file", "", "file containing a GitHub token for private repositories")
	flags.BoolVar(&options.Force, "force", false, "reinstall even when the selected version is already installed")
	flags.BoolVar(&options.Prerelease, "prerelease", false, "include prereleases when resolving latest")

	return command
}
