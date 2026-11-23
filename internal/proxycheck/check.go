// Package proxycheck detects competing multicast proxies before changing IPTV state.
package proxycheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const statePath = "data/udapi-config/ubios-udapi-server/ubios-udapi-server.state"

var (
	errNative    = errors.New("UniFi's native IGMP Proxy is enabled; disable IGMP Proxy in UniFi Network > Internet > your WAN and save, then retry (IGMP snooping can remain enabled)")
	errCompeting = errors.New("another multicast proxy is running; disable its managing service before retrying; for UniFi's proxy, disable IGMP Proxy in UniFi Network > Internet > your WAN")
)

// Check reads UniFi's effective configuration and processes in this network
// namespace. It never stops services or probes MRT_INIT, which would claim the
// kernel's multicast routing socket. A later competing startup remains possible.
func Check(ctx context.Context) error {
	group := sync.OnceValues(func() (string, error) { return serviceGroup(ctx) })
	return check(ctx, "/", group)
}

func check(ctx context.Context, root string, group func() (string, error)) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("check multicast proxy availability: %w", err)
	}
	if err := checkNative(root); err != nil {
		return err
	}
	namespace, err := os.Readlink(filepath.Join(root, "proc/self/ns/net"))
	if err != nil {
		return fmt.Errorf("read own network namespace: %w", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "proc"))
	if err != nil {
		return fmt.Errorf("inspect multicast proxy processes: %w", err)
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("check multicast proxy availability: %w", err)
		}
		if err := checkProcess(root, entry.Name(), namespace, group); err != nil {
			if disappearedProcess(err) {
				continue // The process disappeared while we inspected it.
			}
			return err
		}
	}
	return nil
}

func disappearedProcess(err error) bool {
	return errors.Is(err, os.ErrNotExist) && !errors.Is(err, errCompeting)
}

func checkNative(root string) error {
	data, err := os.ReadFile(filepath.Join(root, statePath))
	if errors.Is(err, os.ErrNotExist) {
		return nil // Other Linux systems, or UniFi has not generated its state yet.
	}
	if err != nil {
		return fmt.Errorf("read UniFi IGMP Proxy state: %w", err)
	}
	var state struct {
		Services struct {
			IGMPProxy struct {
				Enabled bool `json:"enabled"`
			} `json:"igmpProxy"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode UniFi IGMP Proxy state: %w", err)
	}
	if state.Services.IGMPProxy.Enabled {
		return errNative
	}
	return nil
}

func checkProcess(root, pid, namespace string, group func() (string, error)) error {
	base := filepath.Join(root, "proc", pid)
	comm, err := os.ReadFile(filepath.Join(base, "comm"))
	if err != nil {
		return fmt.Errorf("read process %s name: %w", pid, err)
	}
	name := strings.TrimSpace(string(comm))
	if name != "improxy" && name != "igmpproxy" {
		return nil
	}
	theirs, err := os.Readlink(filepath.Join(base, "ns/net"))
	if err != nil {
		return fmt.Errorf("read process %s network namespace: %w", pid, err)
	}
	if namespace != theirs {
		return nil
	}
	cgroup, err := os.ReadFile(filepath.Join(base, "cgroup"))
	if err != nil {
		return fmt.Errorf("read process %s service ownership: %w", pid, err)
	}
	unit, err := group()
	if err != nil {
		return fmt.Errorf("%w (%s, PID %s): %w", errCompeting, name, pid, err)
	}
	if ownedService(string(cgroup), unit) {
		return nil // Both v4 migration and v5 restart must allow our running proxy.
	}
	return fmt.Errorf("%w (%s, PID %s)", errCompeting, name, pid)
}

func ownedService(cgroups, unit string) bool {
	if unit == "" || unit == "/" {
		return false
	}
	for line := range strings.SplitSeq(cgroups, "\n") {
		_, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		controller, path, ok := strings.Cut(rest, ":")
		if !ok || (controller != "" && controller != "name=systemd") {
			continue
		}
		if path == unit || strings.HasPrefix(path, unit+"/") {
			return true
		}
	}
	return false
}
