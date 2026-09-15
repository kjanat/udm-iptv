package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/installer"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/spf13/cobra"
)

const (
	unitPath       = "/etc/systemd/system/udm-iptv.service"
	tmpfilesPath   = "/etc/tmpfiles.d/udm-iptv.conf"
	commandPath    = "/usr/local/bin/udm-iptv"
	completionPath = "/etc/bash_completion.d/udm-iptv"
)

func (application *Application) installCommand() *cobra.Command {
	return application.installCommandWith(installDependencies{
		load: func() (config.Config, error) { return config.Load(application.ConfigPath) },
		legacy: func() (config.Config, bool, error) {
			return importFirstLegacy([]string{"/etc/udm-iptv.conf", filepath.Join(application.StateDir, "legacy.conf")})
		},
		defaults: detectedDefaults, prompt: application.configureForm,
		executable: os.Executable, requireRoot: requireRoot,
		backend: installationBackend{application},
	})
}

// Read-only discovery, interactive input and host mutation are separate seams.
type installDependencies struct {
	load        func() (config.Config, error)
	legacy      func() (config.Config, bool, error)
	defaults    func() config.Config
	prompt      func(context.Context, *config.Config) error
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
			if !dryRun {
				if err := deps.requireRoot(); err != nil {
					return err
				}
			}
			value, err := deps.load()
			save := false
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
				}
				save = true
			}
			if !nonInteractive && (save || dryRun) {
				if dryRun {
					if err := writeString(application.Out, "Preview mode: changes made in this wizard will not be saved or applied.\n"); err != nil {
						return err
					}
				}
				if err := deps.prompt(command.Context(), &value); err != nil {
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
			return writef(application.Out, "udm-iptv %s has started and is enabled to start after reboot. Installed in %s.\n", application.Version, application.StateDir)
		},
	}
	command.Flags().BoolVar(&replace, "force", false, "replace an existing persistent installation")
	command.Flags().BoolVar(&nonInteractive, "non-interactive", false, "install using the existing, imported, or detected configuration")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "preview without system changes, service checks or telemetry")
	return command
}

func importFirstLegacy(paths []string) (config.Config, bool, error) {
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return config.Config{}, false, fmt.Errorf("inspect legacy configuration %s: %w", path, err)
		}
		value, err := config.ImportLegacy(path)
		if err != nil {
			return config.Config{}, false, fmt.Errorf("import legacy configuration %s: %w", path, err)
		}
		return value, true, nil
	}
	return config.Config{}, false, nil
}

type installationBackend struct{ application *Application }

func (backend installationBackend) Apply(ctx context.Context, action installer.Action, plan installer.Plan) error {
	application := backend.application
	target := filepath.Join(plan.StateDir, "bin", "udm-iptv")
	switch action {
	case installer.Preflight:
		if installed(plan.StateDir) && !plan.Replace && !sameFile(plan.Executable, target) {
			return errors.New("udm-iptv is already installed; use --force to replace it")
		}
		return nil
	case installer.SaveConfig:
		if err := config.Save(plan.ConfigPath, plan.Config); err != nil {
			return err
		}
		application.reportConfig = &plan.Config
		return nil
	case installer.RemoveLegacy:
		return removeLegacyPackage(ctx, application.Out, application.Err)
	case installer.CopyBinary:
		return copyExecutable(plan.Executable, target)
	case installer.WriteFiles:
		unit := systemdUnit(target, plan.ConfigPath, plan.StateDir)
		if err := atomicWrite(unitPath, []byte(unit), 0o644); err != nil {
			return err
		}
		tmpfiles := fmt.Sprintf("L+ %s - - - - %s\n", commandPath, target)
		if err := atomicWrite(tmpfilesPath, []byte(tmpfiles), 0o644); err != nil {
			return err
		}
		if err := replaceSymlink(target, commandPath); err != nil {
			return err
		}
		if err := replaceSymlink(target, filepath.Join(plan.StateDir, "bin", "udhcpc-hook")); err != nil {
			return err
		}
		var completion bytes.Buffer
		if err := application.root().GenBashCompletion(&completion); err != nil {
			return fmt.Errorf("install Bash completion: %w", err)
		}
		if err := atomicWrite(completionPath, completion.Bytes(), 0o644); err != nil {
			return fmt.Errorf("install Bash completion: %w", err)
		}
		return nil
	case installer.Activate:
		connection, err := systemd.NewSystemConnectionContext(ctx)
		if err != nil {
			return fmt.Errorf("connect to systemd: %w", err)
		}
		defer connection.Close()
		if err := connection.ReloadContext(ctx); err != nil {
			return fmt.Errorf("reload systemd: %w", err)
		}
		if _, _, err := connection.EnableUnitFilesContext(ctx, []string{unitPath}, false, true); err != nil {
			return fmt.Errorf("enable service: %w", err)
		}
		return restartAndWait(ctx, connection, "udm-iptv.service")
	case installer.CheckHealth:
		if err := application.waitHealthy(ctx, 30*time.Second, 6*time.Second); err != nil {
			application.reportHealthFailure(ctx)
			return fmt.Errorf("installation completed but the service is unhealthy: %w", err)
		}
		application.reportConfig, application.reportApplied = &plan.Config, true
		return nil
	case installer.Cleanup:
		return removeObsoleteLegacyFiles(plan.StateDir)
	default:
		return fmt.Errorf("unknown installation action %q", action)
	}
}

func removeObsoleteLegacyFiles(stateDir string) error {
	for _, obsolete := range []string{
		"/etc/systemd/system/udm-iptv-restore.service",
		"/etc/systemd/system/multi-user.target.wants/udm-iptv-restore.service",
		filepath.Join(stateDir, "udm-iptv-restore"),
		filepath.Join(stateDir, "udm-iptv.deb"),
		filepath.Join(stateDir, "debconf.preseed"),
		filepath.Join(stateDir, "udm-iptv.conf"),
		filepath.Join(stateDir, "legacy.conf"),
	} {
		if err := os.Remove(obsolete); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove obsolete installation file %s: %w", obsolete, err)
		}
	}
	return nil
}

func removeLegacyPackage(ctx context.Context, output, errorOutput io.Writer) error {
	if _, err := os.Stat("/usr/share/udm-iptv/go-package"); err == nil {
		return nil
	}
	query := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${db:Status-Status}", "udm-iptv")
	status, err := query.Output()
	if err != nil || strings.TrimSpace(string(status)) != "installed" {
		return nil
	}
	if err := writeString(output, "Removing the legacy Debian package after importing its configuration...\n"); err != nil {
		return err
	}
	remove := exec.CommandContext(ctx, "dpkg", "--remove", "udm-iptv")
	remove.Stdout, remove.Stderr = output, errorOutput
	if err := remove.Run(); err != nil {
		return fmt.Errorf("remove legacy package: %w", err)
	}
	return nil
}

func systemdUnit(target, configPath, stateDir string) string {
	return fmt.Sprintf(`[Unit]
Description=Routed IPTV for UniFi OS
After=network-online.target
Wants=network-online.target
Conflicts=igmpproxy.service

[Service]
Type=notify
NotifyAccess=main
Environment=%s
ExecStart=%s daemon --config %s
Restart=on-failure
RestartSec=5s
TimeoutStartSec=45s
TimeoutStopSec=30s

[Install]
WantedBy=multi-user.target
`, systemdQuote("UDM_IPTV_STATE_DIR="+stateDir), systemdQuote(target), systemdQuote(configPath))
}

func systemdQuote(value string) string {
	value = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(value)
	return `"` + value + `"`
}

func (application *Application) uninstallCommand() *cobra.Command {
	var keepConfig bool
	command := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove udm-iptv from this console",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			ctx := command.Context()
			connection, err := systemd.NewSystemConnectionContext(ctx)
			if err != nil {
				return fmt.Errorf("connect to systemd before uninstall: %w", err)
			}
			defer connection.Close()
			if err := stopAndWait(ctx, connection, "udm-iptv.service"); err != nil && !noSuchUnit(err) {
				return fmt.Errorf("stop service before uninstall: %w", err)
			}
			if _, err := connection.DisableUnitFilesContext(ctx, []string{"udm-iptv.service"}, false); err != nil {
				return fmt.Errorf("disable service before uninstall: %w", err)
			}
			if value, err := config.Load(application.ConfigPath); err == nil {
				_ = network.RemoveNAT(value)
			}
			for _, path := range []string{unitPath, tmpfilesPath, commandPath, completionPath} {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			if keepConfig {
				for _, entry := range []string{"bin", "diagnostics"} {
					if err := os.RemoveAll(filepath.Join(application.StateDir, entry)); err != nil {
						return err
					}
				}
			} else if err := os.RemoveAll(application.StateDir); err != nil {
				return err
			}
			if err := connection.ReloadContext(ctx); err != nil {
				return fmt.Errorf("reload systemd after uninstall: %w", err)
			}
			return writeString(application.Out, "udm-iptv removed.\n")
		},
	}
	command.Flags().BoolVar(&keepConfig, "keep-config", false, "retain the configuration in /data")
	return command
}

func noSuchUnit(err error) bool {
	var dbusError *dbus.Error
	return errors.As(err, &dbusError) && dbusError.Name == "org.freedesktop.systemd1.NoSuchUnit"
}

func stopAndWait(ctx context.Context, connection *systemd.Conn, unit string) error {
	result := make(chan string, 1)
	if _, err := connection.StopUnitContext(ctx, unit, "replace", result); err != nil {
		return err
	}
	select {
	case status := <-result:
		if status != "done" {
			return fmt.Errorf("stop finished with status %s", status)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
	result := make(chan string, 1)
	if _, err := connection.RestartUnitContext(ctx, "udm-iptv.service", "replace", result); err != nil {
		return err
	}
	select {
	case status := <-result:
		if status != "done" {
			return fmt.Errorf("restart finished with status %s", status)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if verify {
		if err := application.waitHealthy(ctx, 30*time.Second, 6*time.Second); err != nil {
			application.reportHealthFailure(ctx)
			return err
		}
	}
	return nil
}

func restartAndWait(ctx context.Context, connection *systemd.Conn, unit string) error {
	result := make(chan string, 1)
	if _, err := connection.RestartUnitContext(ctx, unit, "replace", result); err != nil {
		return err
	}
	select {
	case status := <-result:
		if status != "done" {
			return fmt.Errorf("start finished with status %s", status)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func copyExecutable(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer closeIgnoringError(input)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".udm-iptv-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer removeIgnoringError(name)
	if _, err := io.Copy(temporary, input); err != nil {
		closeIgnoringError(temporary)
		return err
	}
	if err := temporary.Chmod(0o755); err != nil {
		closeIgnoringError(temporary)
		return err
	}
	if err := temporary.Sync(); err != nil {
		closeIgnoringError(temporary)
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, target)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".udm-iptv-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer removeIgnoringError(name)
	if err := temporary.Chmod(mode); err != nil {
		closeIgnoringError(temporary)
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		closeIgnoringError(temporary)
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func replaceSymlink(target, path string) error {
	if current, err := os.Readlink(path); err == nil && current == target {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, path)
}

func sameFile(left, right string) bool {
	a, errA := os.Stat(left)
	b, errB := os.Stat(right)
	return errA == nil && errB == nil && os.SameFile(a, b)
}
