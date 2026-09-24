package proxycheck

import (
	"context"
	"errors"
	"fmt"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

// Ask the local manager rather than assuming a host cgroup layout. Firmware
// containers sharing the host cgroup namespace have a Docker scope prefix;
// another container can also have a service named udm-iptv.service.
func serviceGroup(ctx context.Context) (string, error) {
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		return "", fmt.Errorf("connect to systemd to identify our proxy: %w", err)
	}
	defer connection.Close()
	property, err := connection.GetServicePropertyContext(ctx, "udm-iptv.service", "ControlGroup")
	if missingUnit(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read udm-iptv.service cgroup: %w", err)
	}
	var group string
	if err := property.Value.Store(&group); err != nil {
		return "", fmt.Errorf("decode udm-iptv.service cgroup: %w", err)
	}
	return group, nil
}

func missingUnit(err error) bool {
	if pointer, ok := errors.AsType[*dbus.Error](err); ok {
		return missingUnitName(pointer.Name)
	}
	if value, ok := errors.AsType[dbus.Error](err); ok {
		return missingUnitName(value.Name)
	}
	return false
}

func missingUnitName(name string) bool {
	return name == "org.freedesktop.systemd1.NoSuchUnit" || name == "org.freedesktop.DBus.Error.UnknownObject"
}
