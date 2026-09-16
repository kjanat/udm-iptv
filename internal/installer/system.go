package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/service"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/runtimebundle"
)

// SystemBackend applies plans using the host filesystem and systemd.
// Callbacks connect completion generation, health checks and reporting.
type SystemBackend struct {
	Out, Err       io.Writer
	Completion     func(context.Context) ([]byte, error)
	CheckHealth    func(context.Context) error
	Saved, Healthy func(config.Config)
}

const (
	unitPath       = "/etc/systemd/system/udm-iptv.service"
	tmpfilesPath   = "/etc/tmpfiles.d/udm-iptv.conf"
	commandPath    = "/usr/local/bin/udm-iptv"
	completionPath = "/etc/bash_completion.d/udm-iptv"
)

func PreserveProxy(stateDir, program string) error {
	source, err := exec.LookPath(program)
	if err != nil {
		_, _, cachedErr := runtimebundle.Command(stateDir, program, nil)
		if cachedErr != nil {
			return fmt.Errorf("find %s in PATH or the offline runtime: %w", program, errors.Join(err, cachedErr))
		}
		return nil
	}
	if err := runtimebundle.Preserve(stateDir, program, source); err != nil {
		return fmt.Errorf("preserve %s for firmware recovery: %w", program, err)
	}
	return nil
}

func (backend SystemBackend) Apply(ctx context.Context, action Action, plan Plan) error {
	target := filepath.Join(plan.StateDir, "bin", "udm-iptv")
	switch action {
	case Preflight:
		if Installed(plan.StateDir) && !plan.Replace && !sameFile(plan.Executable, target) {
			return errors.New("udm-iptv is already installed; use --force to replace it")
		}

		return nil
	case SaveConfig:
		err := config.Save(plan.ConfigPath, plan.Config)
		if err != nil {
			return err
		}
		if backend.Saved != nil {
			backend.Saved(plan.Config)
		}

		return nil
	case RemoveLegacy:
		return removeLegacyPackage(ctx, backend.Out, backend.Err)
	case PreserveRuntime:
		return PreserveProxy(plan.StateDir, plan.Config.Proxy.Program)
	case CopyBinary:
		return atomicfile.Copy(plan.Executable, target)
	case WriteFiles:
		return backend.writeFiles(ctx, target, plan)
	case Activate:
		return activateService(ctx)
	case CheckHealth:
		err := backend.CheckHealth(ctx)
		if err != nil {
			return fmt.Errorf("installation completed but the service is unhealthy: %w", err)
		}
		if backend.Healthy != nil {
			backend.Healthy(plan.Config)
		}

		return nil
	case Cleanup:
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
		err := os.Remove(obsolete)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
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
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil // dpkg-query reports an unknown package with exit code 1.
		}

		return fmt.Errorf("inspect legacy package: %w", err)
	}
	if strings.TrimSpace(string(status)) != "installed" {
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

func replaceSymlink(target, path string) error {
	if current, err := os.Readlink(path); err == nil && current == target {
		return nil
	}
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err = os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		return err
	}

	return os.Symlink(target, path)
}

func sameFile(left, right string) bool {
	a, errA := os.Stat(left)
	b, errB := os.Stat(right)

	return errA == nil && errB == nil && os.SameFile(a, b)
}

func Installed(stateDir string) bool {
	info, err := os.Stat(filepath.Join(stateDir, "bin", "udm-iptv"))

	return err == nil && info.Mode().IsRegular()
}

func (backend SystemBackend) writeFiles(ctx context.Context, target string, plan Plan) error {
	unit := systemdUnit(target, plan.ConfigPath, plan.StateDir)
	if err := atomicfile.Write(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	tmpfiles := fmt.Sprintf("L+ %s - - - - %s\n", commandPath, target)
	if err := atomicfile.Write(tmpfilesPath, []byte(tmpfiles), 0o644); err != nil {
		return err
	}
	if err := replaceSymlink(target, commandPath); err != nil {
		return err
	}
	if err := replaceSymlink(target, filepath.Join(plan.StateDir, "bin", "udhcpc-hook")); err != nil {
		return err
	}
	completion, err := backend.Completion(ctx)
	if err != nil {
		return fmt.Errorf("install Bash completion: %w", err)
	}
	if err := atomicfile.Write(completionPath, completion, 0o644); err != nil {
		return fmt.Errorf("install Bash completion: %w", err)
	}

	return nil
}

func activateService(ctx context.Context) error {
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

	return service.Restart(ctx, connection, "udm-iptv.service")
}
