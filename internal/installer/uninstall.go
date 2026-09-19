package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	systemd "github.com/coreos/go-systemd/v22/dbus"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/kjanat/udm-iptv/internal/service"
)

// Uninstall stops the service before removing its installation.
func Uninstall(ctx context.Context, configPath, stateDir string, keepConfig bool) (result error) {
	root, err := openStateDirectory(stateDir)
	if err != nil && !errors.Is(err, errStateDirectoryMissing) {
		return err
	}
	if root != nil {
		defer func() { result = errors.Join(result, root.Close()) }()
		release, err := AcquireLock(stateDir)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, release()) }()
	}
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		return fmt.Errorf("connect to systemd before uninstall: %w", err)
	}
	defer connection.Close()
	remover := uninstaller{connection: connection, root: root, configPath: configPath, stateDir: stateDir, keepConfig: keepConfig}
	return executeUninstall(ctx, uninstallActions{
		stop:        remover.stopService,
		disable:     remover.disableService,
		removeNAT:   remover.removeNAT,
		removeFiles: remover.removeServiceFiles,
		removeState: remover.removeInstallationState,
		reload:      connection.ReloadContext,
	})
}

type uninstaller struct {
	connection *systemd.Conn
	root       *os.Root
	configPath string
	stateDir   string
	keepConfig bool
}

func (u uninstaller) stopService(ctx context.Context) error {
	err := service.Stop(ctx, u.connection, "udm-iptv.service")
	if service.NoSuchUnit(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("udm-iptv.service did not stop: %w", err)
	}
	return nil
}

func (u uninstaller) disableService(ctx context.Context) error {
	if _, err := u.connection.DisableUnitFilesContext(ctx, []string{"udm-iptv.service"}, false); err != nil {
		return fmt.Errorf("systemd kept udm-iptv.service enabled: %w", err)
	}
	return nil
}

func (u uninstaller) removeNAT(context.Context) error {
	value, err := config.Load(u.configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load configuration for NAT cleanup: %w", err)
	}
	if _, err := network.RemoveNAT(value); err != nil {
		return fmt.Errorf("delete nat POSTROUTING masquerade rules: %w", err)
	}
	return nil
}

func (u uninstaller) removeServiceFiles(context.Context) error {
	for _, path := range []string{unitPath, tmpfilesPath, commandPath, completionPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	return nil
}

func (u uninstaller) removeInstallationState(context.Context) error {
	if err := removeStateFiles(u.root, u.configPath, u.keepConfig); err != nil {
		return err
	}
	if u.keepConfig || u.root == nil {
		return nil
	}
	return ignoreNonEmpty(os.Remove(u.stateDir)) // Empty directories only; never recursive.
}

type uninstallActions struct {
	stop, disable, removeNAT, removeFiles, removeState, reload func(context.Context) error
}

// CleanupError reports that the installation is gone but an optional cleanup
// step failed. Callers that must not fail on a warning check for it.
type CleanupError struct{ Err error }

func (warning CleanupError) Error() string { return warning.Err.Error() }

func (warning CleanupError) Unwrap() error { return warning.Err }

func executeUninstall(ctx context.Context, actions uninstallActions) error {
	var deferred error
	for _, step := range []struct {
		name     string
		run      func(context.Context) error
		optional bool
	}{
		{"stop service", actions.stop, false},
		{"disable service", actions.disable, false},
		{"remove NAT rules", actions.removeNAT, true},
		{"remove service files", actions.removeFiles, false},
		{"remove installation state", actions.removeState, false},
		{"reload systemd", actions.reload, false},
	} {
		if err := ctx.Err(); err != nil {
			return errors.Join(deferred, err)
		}
		err := step.run(ctx)
		if err == nil {
			continue
		}
		err = fmt.Errorf("uninstall: %s: %w", step.name, err)
		if step.optional {
			deferred = errors.Join(deferred, err)
			continue
		}
		return errors.Join(deferred, err)
	}
	if deferred != nil {
		return CleanupError{Err: deferred}
	}
	return nil
}

// packageName is the Debian package that ships this program.
const packageName = "udm-iptv"

// packageCommands are the external programs a delegated removal or upgrade runs.
type packageCommands struct {
	record  func(context.Context) (PackageRecord, error)
	remove  func(ctx context.Context, action string, out, errOut io.Writer) error
	install func(ctx context.Context, packagePath string, out, errOut io.Writer) error
}

func systemPackageCommands() packageCommands {
	return packageCommands{record: QueryPackage, remove: aptRemove, install: aptInstall}
}

// PackageRecord is what dpkg holds for the udm-iptv package: its status word
// and the version it last unpacked. A zero record means dpkg knows nothing.
type PackageRecord struct {
	Status  string
	Version string
}

// Owned reports whether dpkg still needs to take the package apart.
func (record PackageRecord) Owned() bool {
	return packageOwned(record.Status)
}

// ReleaseVersion is the recorded version as releases spell it. nfpm writes
// a prerelease separator as ~ so that dpkg sorts it before the release.
func (record PackageRecord) ReleaseVersion() string {
	return strings.ReplaceAll(record.Version, "~", "-")
}

// QueryPackage asks dpkg what it holds for this installation. The marker
// file alone cannot answer that, because a firmware update or a partial
// removal can leave one behind without a package to match.
func QueryPackage(ctx context.Context) (PackageRecord, error) {
	query := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${db:Status-Status}\t${Version}", packageName)
	output, err := query.Output()
	if err == nil {
		status, version, _ := strings.Cut(strings.TrimSpace(string(output)), "\t")

		return PackageRecord{Status: status, Version: version}, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return PackageRecord{}, nil // dpkg-query reports an unknown package with exit code 1.
	}
	if errors.Is(err, exec.ErrNotFound) {
		return PackageRecord{}, nil // A console without dpkg cannot own this installation.
	}

	return PackageRecord{}, fmt.Errorf("inspect the %s package: %w", packageName, err)
}

// packageOwned reports whether a dpkg status word still needs dpkg to take
// the package apart: unpacked, half-configured, half-installed and the
// trigger states as much as installed. In config-files state dpkg has run
// prerm already and holds no file this program installs afterwards, and
// not-installed is how dpkg-query reports a purged package it remembers.
func packageOwned(status string) bool {
	switch status {
	case "", "not-installed", "config-files":
		return false
	default:
		return true
	}
}

func aptRemove(ctx context.Context, action string, out, errOut io.Writer) error {
	return runApt(ctx, out, errOut, action, "-y", packageName)
}

func aptInstall(ctx context.Context, packagePath string, out, errOut io.Writer) error {
	return runApt(ctx, out, errOut, "install", "-y", packagePath)
}

func runApt(ctx context.Context, out, errOut io.Writer, arguments ...string) error {
	apt := exec.CommandContext(ctx, "apt-get", arguments...)
	apt.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	apt.Stdout, apt.Stderr = out, errOut
	if err := apt.Run(); err != nil {
		return fmt.Errorf("apt-get %s: %w", strings.Join(arguments, " "), err)
	}

	return nil
}

// DelegateRemoval hands a dpkg-tracked installation to apt, which runs prerm
// and reaches the internal cleanup from there. It reports whether it
// delegated, and runs before anything is deleted, so a failure leaves the
// installation whole rather than half removed behind dpkg's back.
func DelegateRemoval(ctx context.Context, keepConfig bool, out, errOut io.Writer) (bool, error) {
	return delegateRemoval(ctx, keepConfig, out, errOut, systemPackageCommands())
}

func delegateRemoval(ctx context.Context, keepConfig bool, out, errOut io.Writer, commands packageCommands) (bool, error) {
	record, err := commands.record(ctx)
	if err != nil || !record.Owned() {
		return false, err
	}
	action := "purge"
	if keepConfig {
		action = "remove"
	}
	if err := writeString(out, "Removing the "+packageName+" package with apt-get "+action+"...\n"); err != nil {
		return false, err
	}
	if err := commands.remove(ctx, action, out, errOut); err != nil {
		return false, err
	}

	return true, nil
}
