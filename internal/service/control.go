package service

import (
	"context"
	"errors"
	"fmt"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

var (
	errStopNotDone    = errors.New("stop finished with status")
	errRestartNotDone = errors.New("start finished with status")
)

// NoSuchUnit reports whether err is systemd's "unit not found" D-Bus error.
func NoSuchUnit(err error) bool {
	var dbusError *dbus.Error

	return errors.As(err, &dbusError) && dbusError.Name == "org.freedesktop.systemd1.NoSuchUnit"
}

// Stop stops unit and waits for systemd to report the job finished.
func Stop(ctx context.Context, connection *systemd.Conn, unit string) error {
	result := make(chan string, 1)
	if _, err := connection.StopUnitContext(ctx, unit, "replace", result); err != nil {
		return fmt.Errorf("stop %s: %w", unit, err)
	}
	select {
	case status := <-result:
		if status != "done" {
			return fmt.Errorf("%w %s", errStopNotDone, status)
		}

		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop %s: %w", unit, ctx.Err())
	}
}

// Restart restarts unit and waits for systemd to report the job finished.
func Restart(ctx context.Context, connection *systemd.Conn, unit string) error {
	result := make(chan string, 1)
	if _, err := connection.RestartUnitContext(ctx, unit, "replace", result); err != nil {
		return fmt.Errorf("restart %s: %w", unit, err)
	}
	select {
	case status := <-result:
		if status != "done" {
			return fmt.Errorf("%w %s", errRestartNotDone, status)
		}

		return nil
	case <-ctx.Done():
		return fmt.Errorf("restart %s: %w", unit, ctx.Err())
	}
}
