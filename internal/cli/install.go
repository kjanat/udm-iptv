package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/device"
	"github.com/kjanat/udm-iptv/internal/installer"
	"github.com/kjanat/udm-iptv/internal/service"
)

const (
	restartHealthStartup = 30 * time.Second
	restartHealthStable  = 6 * time.Second
)

func (application *Application) installCommand() *cobra.Command {
	return application.installCommandWith(installDependencies{
		load: func() (config.Config, error) { return config.Load(application.ConfigPath) },
		legacy: func() (config.Config, bool, error) {
			return config.ImportFirstLegacy(legacyCandidates(application.StateDir))
		},
		defaults: device.Defaults, prompt: application.configureForm,
		promptFresh: application.configureFreshForm,
		executable:  os.Executable, requireRoot: requireRoot,
		backend: application.installBackend(),
	})
}

// promptForInstall warns about a dry run, then prompts for the configuration.
// A fresh install asks the reporting question before anything is looked up. A
// dry run looks nothing up, because it saves and applies nothing.
func promptForInstall(ctx context.Context, deps installDependencies, out io.Writer, value *config.Config, fresh, dryRun bool) error {
	if dryRun {
		if err := writeString(out, "Preview: nothing will be saved or applied.\n"); err != nil {
			return err
		}
	}
	if fresh && !dryRun && deps.promptFresh != nil {
		return deps.promptFresh(ctx, value)
	}

	return deps.prompt(ctx, value)
}

type installSource struct {
	value config.Config
	save  bool
	fresh bool
}

func selectInstallConfig(deps installDependencies) (installSource, error) {
	value, err := deps.load()
	if err == nil {
		return installSource{value: value}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return installSource{}, err
	}
	legacy, found, err := deps.legacy()
	if err != nil {
		return installSource{}, err
	}
	if found {
		return installSource{value: legacy, save: true}, nil
	}

	return installSource{value: deps.defaults(), save: true, fresh: true}, nil
}

func (application *Application) installPlan(deps installDependencies, source installSource, replace bool) (installer.Plan, error) {
	executable, err := deps.executable()
	if err != nil {
		return installer.Plan{}, err
	}
	configPath, err := filepath.Abs(application.ConfigPath)
	if err != nil {
		return installer.Plan{}, fmt.Errorf("resolve configuration path %s: %w", application.ConfigPath, err)
	}
	stateDir, err := filepath.Abs(application.StateDir)
	if err != nil {
		return installer.Plan{}, fmt.Errorf("resolve state directory %s: %w", application.StateDir, err)
	}

	return installer.Plan{
		Config: source.value, ConfigPath: configPath, StateDir: stateDir,
		Executable: executable, SaveConfig: source.save, Replace: replace,
	}, nil
}

// Read-only discovery, interactive input and host mutation are separate seams.
type installDependencies struct {
	load        func() (config.Config, error)
	legacy      func() (config.Config, bool, error)
	defaults    func() config.Config
	prompt      func(context.Context, *config.Config) error
	promptFresh func(context.Context, *config.Config) error
	executable  func() (string, error)
	requireRoot func() error
	backend     installer.Backend
}

type installOptions struct {
	nonInteractive bool
	replace        bool
	dryRun         bool
}

func (application *Application) installCommandWith(deps installDependencies) *cobra.Command {
	var options installOptions
	command := &cobra.Command{
		Use:   commandInstall,
		Short: "Install the persistent service on this console",
		Args:  cobra.NoArgs,
		RunE: application.reportingSaved(commandInstall, previewOnly, func(command *cobra.Command, _ []string) error {
			return application.runInstall(command, deps, options)
		}),
	}
	command.Flags().BoolVar(&options.replace, "force", false, "replace an existing persistent installation")
	command.Flags().BoolVar(&options.nonInteractive, "non-interactive", false, "install using the existing, imported, or detected configuration")
	command.Flags().BoolVar(&options.dryRun, "dry-run", false, "preview without system changes, service checks or telemetry")

	return command
}

func (application *Application) runInstall(command *cobra.Command, deps installDependencies, options installOptions) error {
	application.providerSuggestion = ""
	source, err := application.prepareInstall(command, deps, options)
	if err != nil {
		return err
	}
	plan, err := application.installPlan(deps, source, options.replace)
	if err != nil {
		return err
	}

	return application.applyInstall(command, deps, plan, options)
}

func (application *Application) prepareInstall(command *cobra.Command, deps installDependencies, options installOptions) (installSource, error) {
	if !options.dryRun {
		if err := deps.requireRoot(); err != nil {
			return installSource{}, err
		}
	}
	source, err := selectInstallConfig(deps)
	if err != nil {
		return installSource{}, err
	}
	if options.nonInteractive || !source.save && !options.dryRun {
		return source, nil
	}
	if err := promptForInstall(command.Context(), deps, application.Out, &source.value, source.fresh, options.dryRun); err != nil {
		return installSource{}, err
	}
	source.save = true

	return source, nil
}

func (application *Application) applyInstall(command *cobra.Command, deps installDependencies, plan installer.Plan, options installOptions) error {
	if options.dryRun {
		if err := plan.Preview(application.Out); err != nil {
			return fmt.Errorf("preview the installation plan: %w", err)
		}

		return nil
	}
	if err := plan.Execute(command.Context(), deps.backend); err != nil {
		return fmt.Errorf("apply the installation plan: %w", err)
	}

	return writef(application.Out, "udm-iptv %s has started. Automatic startup enabled.\nInstalled: %s\n", application.Version, application.StateDir)
}

func (application *Application) uninstallCommand() *cobra.Command {
	var keepConfig, fromPackage bool
	command := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove udm-iptv from this console",
		Args:  cobra.NoArgs,
		RunE: application.reporting("uninstall", func(command *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			if !fromPackage {
				delegated, err := installer.DelegateRemoval(command.Context(), keepConfig, application.Out, application.Err)
				if err != nil {
					return fmt.Errorf("remove the udm-iptv package: %w", err)
				}
				if delegated {
					return writeString(application.Out, "udm-iptv removed.\n")
				}
			}

			return application.removeInstallation(command, keepConfig)
		}),
	}
	command.Flags().BoolVar(&keepConfig, "keep-config", false, "retain the configuration in /data")
	command.Flags().BoolVar(&fromPackage, "from-package", false, "remove the installation without calling the package manager")
	_ = command.Flags().MarkHidden("from-package")

	return command
}

// removeInstallation is the cleanup a package maintainer script reaches
// through --from-package. It touches only what install created and never calls
// the package manager, so dpkg stays in charge of the files it shipped.
func (application *Application) removeInstallation(command *cobra.Command, keepConfig bool) error {
	var cleanup installer.CleanupError
	err := installer.Uninstall(command.Context(), application.ConfigPath, application.StateDir, keepConfig)
	switch {
	case errors.As(err, &cleanup):
		if err := writef(application.Err, "Warning: %s\n", cleanup.Err); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("remove the installation in %s: %w", application.StateDir, err)
	}

	return writeString(application.Out, "udm-iptv removed.\n")
}

func (application *Application) restartCommand() *cobra.Command {
	return &cobra.Command{
		Use: "restart", Short: "Restart the IPTV service", Args: cobra.NoArgs,
		RunE: application.reporting("restart", func(command *cobra.Command, _ []string) error {
			return application.restart(command.Context(), true)
		}),
	}
}

func (application *Application) restart(ctx context.Context, verify bool) error {
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		return fmt.Errorf("connect to systemd: %w", err)
	}
	defer connection.Close()
	if err := service.Restart(ctx, connection, "udm-iptv.service"); err != nil {
		return fmt.Errorf("restart udm-iptv.service: %w", err)
	}
	if verify {
		err := application.waitHealthy(ctx, restartHealthStartup, restartHealthStable)
		if err != nil {
			return errors.Join(err, application.collector().ReportFailure(ctx, application.Err))
		}
	}

	return nil
}

func (application *Application) installBackend() installer.Backend {
	return installer.SystemBackend{
		Out: application.Out, Err: application.Err,
		Completion: func(ctx context.Context) ([]byte, error) {
			root := application.root() //nolint:contextcheck // Constructs descriptions; no command runs.
			root.SetContext(ctx)

			return bashCompletionScript(root)
		},
		Health: func(ctx context.Context) error {
			err := application.waitHealthy(ctx, restartHealthStartup, restartHealthStable)
			if err != nil {
				return errors.Join(err, application.collector().ReportFailure(ctx, application.Err))
			}

			return err
		},
		Saved:   func(value config.Config) { application.reportConfig = &value },
		Healthy: func(value config.Config) { application.reportConfig, application.reportApplied = &value, true },
	}
}
