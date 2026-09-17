package installer

import (
	"context"
	"errors"
	"fmt"
	"os"

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
	if err := network.RemoveNAT(value); err != nil {
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
	return deferred
}
