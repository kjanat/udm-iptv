package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

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
	return executeUninstall(ctx, uninstallActions{
		stop: func(ctx context.Context) error {
			err := service.Stop(ctx, connection, "udm-iptv.service")
			if service.NoSuchUnit(err) {
				return nil
			}
			return err
		},
		disable: func(ctx context.Context) error {
			_, err := connection.DisableUnitFilesContext(ctx, []string{"udm-iptv.service"}, false)
			return err
		},
		removeNAT: func(context.Context) error {
			value, err := config.Load(configPath)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("load configuration for NAT cleanup: %w", err)
			}
			return network.RemoveNAT(value)
		},
		removeFiles: func(context.Context) error {
			for _, path := range []string{unitPath, tmpfilesPath, commandPath, completionPath} {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("remove %s: %w", path, err)
				}
			}
			return nil
		},
		removeState: func(context.Context) error {
			if err := removeStateFiles(root, configPath, keepConfig); err != nil {
				return err
			}
			if !keepConfig && root != nil {
				err := os.Remove(stateDir) // Empty directories only; never recursive.
				if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
					return err
				}
			}
			return nil
		},
		reload: connection.ReloadContext,
	})
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
