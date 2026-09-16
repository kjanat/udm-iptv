package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/kjanat/udm-iptv/internal/device"

	"github.com/kjanat/udm-iptv/internal/installer"
	"github.com/kjanat/udm-iptv/internal/service"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/spf13/cobra"
)

func (application *Application) installCommand() *cobra.Command {
	return application.installCommandWith(installDependencies{
		load: func() (config.Config, error) { return config.Load(application.ConfigPath) },
		legacy: func() (config.Config, bool, error) {
			return config.ImportFirstLegacy([]string{"/etc/udm-iptv.conf", filepath.Join(application.StateDir, "legacy.conf")})
		},
		defaults: device.Defaults, prompt: application.configureForm,
		suggest:    application.suggestProvider,
		executable: os.Executable, requireRoot: requireRoot,
		backend: application.installBackend(),
	})
}

// Read-only discovery, interactive input and host mutation are separate seams.
type installDependencies struct {
	load        func() (config.Config, error)
	legacy      func() (config.Config, bool, error)
	defaults    func() config.Config
	prompt      func(context.Context, *config.Config) error
	suggest     func(context.Context, config.Config) error
	executable  func() (string, error)
	requireRoot func() error
	backend     installer.Backend
}

func (application *Application) installCommandWith(deps installDependencies) *cobra.Command {
	var nonInteractive, replace, dryRun bool
	command := &cobra.Command{
		Use:   "install",
		Short: "Install the persistent service on this console",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			application.providerSuggestion = ""
			if !dryRun {
				err := deps.requireRoot()
				if err != nil {
					return err
				}
			}
			value, err := deps.load()
			save := false
			fresh := false
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				legacy, found, legacyErr := deps.legacy()
				if legacyErr != nil {
					return legacyErr
				}
				if found {
					value = legacy
				} else {
					value = deps.defaults()
					fresh = true
				}
				save = true
			}
			if !nonInteractive && (save || dryRun) {
				if fresh && !dryRun && deps.suggest != nil {
					err := deps.suggest(command.Context(), value)
					if err != nil {
						return err
					}
				}
				if dryRun {
					err := writeString(application.Out, "Preview: nothing will be saved or applied.\n")
					if err != nil {
						return err
					}
				}
				err := deps.prompt(command.Context(), &value)
				if err != nil {
					return err
				}
				save = true
			}
			executable, err := deps.executable()
			if err != nil {
				return err
			}
			configPath, err := filepath.Abs(application.ConfigPath)
			if err != nil {
				return err
			}
			stateDir, err := filepath.Abs(application.StateDir)
			if err != nil {
				return err
			}
			plan := installer.Plan{Config: value, ConfigPath: configPath, StateDir: stateDir, Executable: executable, SaveConfig: save, Replace: replace}
			if dryRun {
				return plan.Preview(application.Out)
			}
			if err := plan.Execute(command.Context(), deps.backend); err != nil {
				return err
			}

			return writef(application.Out, "udm-iptv %s has started. Automatic startup enabled.\nInstalled: %s\n", application.Version, application.StateDir)
		},
	}
	command.Flags().BoolVar(&replace, "force", false, "replace an existing persistent installation")
	command.Flags().BoolVar(&nonInteractive, "non-interactive", false, "install using the existing, imported, or detected configuration")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "preview without system changes, service checks or telemetry")

	return command
}

func (application *Application) uninstallCommand() *cobra.Command {
	var keepConfig bool
	command := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove udm-iptv from this console",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			err := requireRoot()
			if err != nil {
				return err
			}
			err = installer.Uninstall(command.Context(), application.ConfigPath, application.StateDir, keepConfig)
			if err != nil {
				return err
			}

			return writeString(application.Out, "udm-iptv removed.\n")
		},
	}
	command.Flags().BoolVar(&keepConfig, "keep-config", false, "retain the configuration in /data")

	return command
}

func (application *Application) restartCommand() *cobra.Command {
	return &cobra.Command{
		Use: "restart", Short: "Restart the IPTV service", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error { return application.restart(command.Context(), true) },
	}
}

func (application *Application) restart(ctx context.Context, verify bool) error {
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := service.Restart(ctx, connection, "udm-iptv.service"); err != nil {
		return err
	}
	if verify {
		err := application.waitHealthy(ctx, 30*time.Second, 6*time.Second)
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
			var output bytes.Buffer
			root := application.root() //nolint:contextcheck // Constructs descriptions; no command runs.
			root.SetContext(ctx)
			err := root.GenBashCompletion(&output)

			return output.Bytes(), err
		},
		CheckHealth: func(ctx context.Context) error {
			err := application.waitHealthy(ctx, 30*time.Second, 6*time.Second)
			if err != nil {
				return errors.Join(err, application.collector().ReportFailure(ctx, application.Err))
			}

			return err
		},
		Saved:   func(value config.Config) { application.reportConfig = &value },
		Healthy: func(value config.Config) { application.reportConfig, application.reportApplied = &value, true },
	}
}
