package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kjanat/udm-iptv/internal/service"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/network"
)

// Uninstall stops the service before removing its installation.
func Uninstall(ctx context.Context, configPath, stateDir string, keepConfig bool) error {
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		return fmt.Errorf("connect to systemd before uninstall: %w", err)
	}
	defer connection.Close()
	if err := service.Stop(ctx, connection, "udm-iptv.service"); err != nil && !service.NoSuchUnit(err) {
		return fmt.Errorf("stop service before uninstall: %w", err)
	}
	if _, err := connection.DisableUnitFilesContext(ctx, []string{"udm-iptv.service"}, false); err != nil {
		return fmt.Errorf("disable service before uninstall: %w", err)
	}
	if value, err := config.Load(configPath); err == nil {
		_ = network.RemoveNAT(value)
	}
	for _, path := range []string{unitPath, tmpfilesPath, commandPath, completionPath} {
		err := os.Remove(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if keepConfig {
		for _, entry := range []string{"bin", "diagnostics"} {
			err := os.RemoveAll(filepath.Join(stateDir, entry))
			if err != nil {
				return err
			}
		}
	} else {
		err := os.RemoveAll(stateDir)
		if err != nil {
			return err
		}
	}
	if err := connection.ReloadContext(ctx); err != nil {
		return fmt.Errorf("reload systemd after uninstall: %w", err)
	}

	return nil
}
