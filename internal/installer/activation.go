package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
)

// Activation is isolated from discovery, downloads and systemd for fault tests.
type upgradeActions struct {
	backup  func(string) (string, error)
	copy    func(string, string) error
	restart func(context.Context, bool) error
	remove  func(string) error
}

func systemUpgradeActions(restart func(context.Context, bool) error) upgradeActions {
	return upgradeActions{backup: backupExecutable, copy: atomicfile.Copy, restart: restart, remove: os.Remove}
}

func backupExecutable(target string) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(target), ".udm-iptv.previous-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		return "", errors.Join(err, os.Remove(name))
	}
	if err := atomicfile.Copy(target, name); err != nil {
		return "", errors.Join(err, os.Remove(name))
	}
	return name, nil
}

func activateUpgrade(ctx context.Context, source, target, version string, actions upgradeActions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	backup, err := actions.backup(target)
	if err != nil {
		return fmt.Errorf("back up current executable: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("upgrade cancelled; backup retained at %s: %w", backup, err)
	}
	if err := actions.copy(source, target); err != nil {
		return fmt.Errorf("replace executable; backup retained at %s: %w", backup, err)
	}
	if activationErr := actions.restart(ctx, true); activationErr != nil {
		if err := actions.copy(backup, target); err != nil {
			return fmt.Errorf("activate %s: %w; restore failed, backup retained at %s: %w", version, activationErr, backup, err)
		}
		// Recovery must survive the failed upgrade's cancellation or deadline.
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := actions.restart(recovery, true); err != nil {
			return fmt.Errorf("activate %s: %w; restart restored executable failed, backup retained at %s: %w", version, activationErr, backup, err)
		}
		return errors.Join(fmt.Errorf("installed %s failed its health check and was rolled back: %w", version, activationErr), removeBackup(actions, backup))
	}
	return removeBackup(actions, backup)
}

func removeBackup(actions upgradeActions, backup string) error {
	if err := actions.remove(backup); err != nil {
		return fmt.Errorf("remove recovery backup %s: %w", backup, err)
	}
	return nil
}
