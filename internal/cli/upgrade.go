package cli

import (
	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/installer"
)

func (application *Application) upgradeCommand() *cobra.Command {
	options := installer.UpgradeOptions{Repository: "kjanat/udm-iptv"}
	command := &cobra.Command{
		Use: "upgrade", Short: "Install the latest udm-iptv release", Args: cobra.NoArgs,
		Long: "Install updates. Preview builds automatically follow prereleases.",
		RunE: application.reportingUnless("upgrade", previewOnly, func(command *cobra.Command, _ []string) error {
			if !options.DryRun {
				if err := requireRoot(); err != nil {
					return err
				}
			}

			return application.upgrader().Upgrade(command.Context(), options)
		}),
	}
	flags := command.Flags()
	flags.StringVar(&options.Repository, "repository", options.Repository, "GitHub repository")
	flags.StringVar(&options.Version, "version", "latest", "release version or latest")
	flags.StringVar(&options.TokenFile, "token-file", "", "file containing a GitHub token for private repositories")
	flags.BoolVar(&options.Force, "force", false, "allow reinstalling the selected version or downgrading")
	flags.BoolVar(&options.Prerelease, "prerelease", false, "include prereleases when resolving latest")
	flags.BoolVar(&options.DryRun, "dry-run", false, "show what would be installed without changing anything")

	return command
}

func (application *Application) upgrader() *installer.Upgrader {
	return &installer.Upgrader{Version: application.Version, StateDir: application.StateDir, Out: application.Out, Err: application.Err, Restart: application.restart}
}
