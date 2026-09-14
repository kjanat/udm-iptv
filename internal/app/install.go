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
	"github.com/kjanat/udm-iptv/internal/config"
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
	var nonInteractive, replace bool
	command := &cobra.Command{
		Use:   "install",
		Short: "Install the persistent service on this console",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			if _, err := config.Load(application.ConfigPath); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				value := detectedDefaults()
				for _, legacyPath := range []string{"/etc/udm-iptv.conf", filepath.Join(application.StateDir, "legacy.conf")} {
					if legacy, legacyErr := config.ImportLegacy(legacyPath); legacyErr == nil {
						value = legacy
						break
					}
				}
				if !nonInteractive {
					if err := application.configureForm(&value); err != nil {
						return err
					}
				}
				if err := config.Save(application.ConfigPath, value); err != nil {
					return err
				}
			}
			return application.install(command.Context(), replace)
		},
	}
	command.Flags().BoolVar(&replace, "force", false, "replace an existing persistent installation")
	command.Flags().BoolVar(&nonInteractive, "non-interactive", false, "install using the existing, imported, or detected configuration")
	return command
}

func (application *Application) install(ctx context.Context, replace bool) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	target := filepath.Join(application.StateDir, "bin", "udm-iptv")
	if installed(application.StateDir) && !replace && !sameFile(executable, target) {
		return errors.New("udm-iptv is already installed; use --force to replace it")
	}
	if err := removeLegacyPackage(ctx, application.Out, application.Err); err != nil {
		return err
	}
	if err := copyExecutable(executable, target); err != nil {
		return fmt.Errorf("install executable: %w", err)
	}
	unit := systemdUnit(target, application.ConfigPath)
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
	if err := replaceSymlink(target, filepath.Join(application.StateDir, "bin", "udhcpc-hook")); err != nil {
		return err
	}
	var completion bytes.Buffer
	if err := application.root().GenBashCompletion(&completion); err != nil {
		return fmt.Errorf("install Bash completion: %w", err)
	}
	if err := atomicWrite(completionPath, completion.Bytes(), 0o644); err != nil {
		return fmt.Errorf("install Bash completion: %w", err)
	}
	for _, obsolete := range []string{
		"/etc/systemd/system/udm-iptv-restore.service",
		"/etc/systemd/system/multi-user.target.wants/udm-iptv-restore.service",
		filepath.Join(application.StateDir, "udm-iptv-restore"),
		filepath.Join(application.StateDir, "udm-iptv.deb"),
		filepath.Join(application.StateDir, "debconf.preseed"),
		filepath.Join(application.StateDir, "udm-iptv.conf"),
		filepath.Join(application.StateDir, "legacy.conf"),
	} {
		if err := os.Remove(obsolete); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove obsolete installation file %s: %w", obsolete, err)
		}
	}
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
	if err := restartAndWait(ctx, connection, "udm-iptv.service"); err != nil {
		return err
	}
	if err := application.waitHealthy(ctx, 30*time.Second, 6*time.Second); err != nil {
		application.reportHealthFailure(ctx)
		return fmt.Errorf("installation completed but the service is unhealthy: %w", err)
	}
	return writef(application.Out, "Installed udm-iptv %s in %s.\n", application.Version, application.StateDir)
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

func systemdUnit(target, configPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Routed IPTV for UniFi OS
After=network-online.target
Wants=network-online.target
Conflicts=igmpproxy.service

[Service]
Type=notify
NotifyAccess=main
ExecStart=%s daemon --config %s
Restart=on-failure
RestartSec=5s
TimeoutStartSec=45s
TimeoutStopSec=30s

[Install]
WantedBy=multi-user.target
`, target, configPath)
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
			if connection, err := systemd.NewSystemConnectionContext(ctx); err == nil {
				_ = stopAndWait(ctx, connection, "udm-iptv.service")
				_, _ = connection.DisableUnitFilesContext(ctx, []string{"udm-iptv.service"}, false)
				connection.Close()
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
			if connection, err := systemd.NewSystemConnectionContext(ctx); err == nil {
				_ = connection.ReloadContext(ctx)
				connection.Close()
			}
			return writeString(application.Out, "udm-iptv removed.\n")
		},
	}
	command.Flags().BoolVar(&keepConfig, "keep-config", false, "retain the configuration in /data")
	return command
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
