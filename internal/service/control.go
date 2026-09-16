package service

import (
	"context"
	"errors"
	"fmt"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

func NoSuchUnit(err error) bool {
	var dbusError *dbus.Error

	return errors.As(err, &dbusError) && dbusError.Name == "org.freedesktop.systemd1.NoSuchUnit"
}

func Stop(ctx context.Context, connection *systemd.Conn, unit string) error {
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

func Restart(ctx context.Context, connection *systemd.Conn, unit string) error {
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
