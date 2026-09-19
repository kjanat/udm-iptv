package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/telemetry"
	"github.com/kjanat/udm-iptv/internal/ui"
)

var (
	errNeedsRoot  = errors.New("this command must be run as root")
	errNeedsLinux = errors.New("this command requires Linux")
)

// Application holds the shared state the udm-iptv CLI commands operate on.
type Application struct {
	Version            string
	ConfigPath         string
	StateDir           string
	In                 io.Reader
	Out                io.Writer
	Err                io.Writer
	monitor            *telemetry.Reporter
	networkIdentity    func(context.Context) telemetry.NetworkIdentity
	seed               func(config.Config) config.Config
	reportConfig       *config.Config
	reportApplied      bool
	providerSuggestion string
}

// Execute runs the udm-iptv root command against os.Args.
func Execute(version string) error {
	application := &Application{
		Version:         version,
		ConfigPath:      env("UDM_IPTV_CONFIG", config.DefaultPath),
		StateDir:        env("UDM_IPTV_STATE_DIR", "/data/udm-iptv"),
		Out:             ui.Styled(os.Stdout),
		In:              os.Stdin,
		Err:             ui.Styled(os.Stderr),
		networkIdentity: telemetry.LookupNetwork,
	}
	executed, err := application.root().ExecuteC()
	if err != nil {
		return fmt.Errorf("%s: %w", executed.CommandPath(), err)
	}

	return nil
}

func (application *Application) root() *cobra.Command {
	command := &cobra.Command{
		Use:           "udm-iptv",
		Short:         "Routed IPTV for UniFi OS",
		Long:          "Configure, run, update, and diagnose routed IPTV on UniFi OS.",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	command.SetOut(application.Out)
	command.SetErr(application.Err)
	plainHelp := command.HelpFunc()
	command.SetHelpFunc(func(current *cobra.Command, arguments []string) {
		var rendered bytes.Buffer
		out := current.OutOrStdout()
		current.SetOut(&rendered)
		plainHelp(current, arguments)
		current.SetOut(out)
		_, _ = io.WriteString(out, ui.HelpText(rendered.String()))
	})
	command.Version = application.Version
	command.SetVersionTemplate("udm-iptv {{.Version}}\n")
	command.CompletionOptions.DisableDefaultCmd = true
	command.PersistentFlags().StringVar(&application.ConfigPath, "config", application.ConfigPath, "configuration file")
	command.AddGroup(
		&cobra.Group{ID: "manage", Title: "Management Commands:"},
		&cobra.Group{ID: "observe", Title: "Observability Commands:"},
	)
	management := []*cobra.Command{
		application.configureCommand(), application.installCommand(), application.startCommand(),
		application.stopCommand(), application.restartCommand(),
		application.uninstallCommand(), application.upgradeCommand(),
		application.previewCommand(),
	}
	for _, child := range management {
		child.GroupID = "manage"
		command.AddCommand(child)
	}
	observability := []*cobra.Command{application.statusCommand(), application.diagnoseCommand(), application.telemetryCommand()}
	for _, child := range observability {
		child.GroupID = "observe"
		command.AddCommand(child)
	}
	versionCommand := &cobra.Command{
		Use: "version", Short: "Print the udm-iptv version", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error { return writef(application.Out, "%s\n", application.Version) },
	}
	command.AddCommand(versionCommand, application.completionCommand(), application.diagnoseWorkerCommand(), application.daemonCommand(), application.dhcpHookCommand())

	return command
}

func completeValues(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}

	return fallback
}

func requireRoot() error {
	if os.Geteuid() != 0 {
		return errNeedsRoot
	}
	if runtime.GOOS != "linux" {
		return errNeedsLinux
	}

	return nil
}
