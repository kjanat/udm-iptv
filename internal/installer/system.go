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

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/filemode"
	"github.com/kjanat/udm-iptv/internal/proxycheck"
	"github.com/kjanat/udm-iptv/internal/runtimebundle"
	"github.com/kjanat/udm-iptv/internal/service"
)

// SystemBackend applies plans using the host filesystem and systemd.
// Callbacks connect completion generation, health checks and reporting.
type SystemBackend struct {
	Out, Err       io.Writer
	Completion     func(context.Context) ([]byte, error)
	Health         func(context.Context) error
	Failure        func(context.Context, error) error
	Saved, Healthy func(config.Config)
}

var errAlreadyInstalled = errors.New("udm-iptv is already installed; use --force to replace it")

const (
	unitPath       = "/etc/systemd/system/udm-iptv.service"
	tmpfilesPath   = "/etc/tmpfiles.d/udm-iptv.conf"
	commandPath    = "/usr/local/bin/udm-iptv"
	completionPath = "/etc/bash_completion.d/udm-iptv"
)

// PreserveProxy snapshots program and its ELF dependencies for offline recovery.
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

func installedExecutable(plan Plan) string {
	return filepath.Join(plan.StateDir, "bin", "udm-iptv")
}

// Preflight checks ownership and proxy conflicts before installation mutations.
func (backend SystemBackend) Preflight(ctx context.Context, plan Plan) error {
	if Installed(plan.StateDir) && !plan.Replace && !sameFile(plan.Executable, installedExecutable(plan)) {
		return errAlreadyInstalled
	}

	if err := proxycheck.Check(ctx); err != nil {
		return fmt.Errorf("check multicast proxy before installation: %w", err)
	}
	return nil
}

// PreserveRuntime snapshots the proxy and its libraries for offline recovery.
func (backend SystemBackend) PreserveRuntime(_ context.Context, plan Plan) error {
	return PreserveProxy(plan.StateDir, plan.Config.Proxy.Program)
}

// SaveConfig writes the planned configuration and reports it as saved.
func (backend SystemBackend) SaveConfig(_ context.Context, plan Plan) error {
	err := config.Save(plan.ConfigPath, plan.Config)
	if err != nil {
		return fmt.Errorf("save configuration to %s: %w", plan.ConfigPath, err)
	}
	if backend.Saved != nil {
		backend.Saved(plan.Config)
	}

	return nil
}

// RemoveLegacy removes the legacy Debian package once its config is imported.
func (backend SystemBackend) RemoveLegacy(ctx context.Context, _ Plan) error {
	return removeLegacyPackage(ctx, backend.Out, backend.Err)
}

// CopyBinary installs the running executable into the state directory.
func (backend SystemBackend) CopyBinary(_ context.Context, plan Plan) error {
	target := installedExecutable(plan)
	if err := atomicfile.Copy(plan.Executable, target); err != nil {
		return fmt.Errorf("install executable at %s: %w", target, err)
	}

	return nil
}

// WriteFiles writes the unit, links, tmpfiles rule and shell completion.
func (backend SystemBackend) WriteFiles(ctx context.Context, plan Plan) error {
	return backend.writeFiles(ctx, installedExecutable(plan), plan)
}

// Activate reloads systemd, enables the unit and restarts the service.
func (backend SystemBackend) Activate(ctx context.Context, plan Plan) error {
	if err := migrateLegacyNetwork(plan, legacyNetworkLinks{find: netlink.LinkByName, setAlias: netlink.LinkSetAlias}); err != nil {
		return err
	}
	err := activateService(ctx)
	if err != nil && backend.Failure != nil {
		// Capture the failed unit before the installation transaction rolls back.
		return backend.Failure(ctx, err)
	}
	return err
}

// CheckHealth waits for readiness. Reporting waits until cleanup also succeeds.
func (backend SystemBackend) CheckHealth(ctx context.Context, _ Plan) error {
	err := backend.Health(ctx)
	if err != nil {
		return fmt.Errorf("installation completed but the service is unhealthy: %w", err)
	}
	return nil
}

// Cleanup removes obsolete legacy recovery files.
func (backend SystemBackend) Cleanup(_ context.Context, plan Plan) error {
	if err := removeObsoleteLegacyFiles(plan.StateDir); err != nil {
		return err
	}
	if backend.Healthy != nil {
		backend.Healthy(plan.Config)
	}
	return nil
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
		filepath.Join(stateDir, legacyNetworkPending),
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
		return fmt.Errorf("remove stale link %s: %w", path, err)
	}
	err = os.MkdirAll(filepath.Dir(path), filemode.SharedDir)
	if err != nil {
		return fmt.Errorf("create parent directory for %s: %w", path, err)
	}
	if err := os.Symlink(target, path); err != nil {
		return fmt.Errorf("link %s to %s: %w", path, target, err)
	}

	return nil
}

func sameFile(left, right string) bool {
	a, errA := os.Stat(left)
	b, errB := os.Stat(right)

	return errA == nil && errB == nil && os.SameFile(a, b)
}

// Installed reports whether the persistent executable under stateDir and its
// systemd unit both exist. dpkg unpacks the executable before postinst
// installs the unit.
func Installed(stateDir string) bool {
	return regularFile(filepath.Join(stateDir, "bin", "udm-iptv")) && regularFile(unitPath)
}

func regularFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}

func (backend SystemBackend) writeFiles(ctx context.Context, target string, plan Plan) error {
	unit := systemdUnit(target, plan.ConfigPath, plan.StateDir)
	if err := atomicfile.Write(unitPath, []byte(unit), filemode.SharedFile); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	tmpfiles := fmt.Sprintf("L+ %s - - - - %s\n", commandPath, target)
	if err := atomicfile.Write(tmpfilesPath, []byte(tmpfiles), filemode.SharedFile); err != nil {
		return fmt.Errorf("write tmpfiles rule: %w", err)
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
	if err := atomicfile.Write(completionPath, completion, filemode.SharedFile); err != nil {
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

	if err := service.Restart(ctx, connection, "udm-iptv.service"); err != nil {
		return fmt.Errorf("restart udm-iptv.service: %w", err)
	}

	return nil
}
