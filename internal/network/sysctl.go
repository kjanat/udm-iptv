package network

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kjanat/udm-iptv/internal/config"
)

// sysctlRoot is the per-interface IPv6 knob directory.
const sysctlRoot = "/proc/sys/net/ipv6/conf"

// A forwarding interface ignores Router Advertisements unless accept_ra is 2.
const acceptRAWhileForwarding = "2"

// Restore puts back the values EnableIPv6Multicast replaced.
type Restore func() error

// EnableIPv6Multicast prepares the interfaces MLD needs and returns the
// function that puts their previous values back.
func EnableIPv6Multicast(value config.Config) (Restore, error) {
	if value.Proxy.MLDVersion == 0 {
		return func() error { return nil }, nil
	}
	previous := map[string]string{}
	restore := func() error {
		var joined error
		for path, was := range previous {
			joined = errors.Join(joined, writeSysctl(path, was))
		}

		return joined
	}
	for path, want := range ipv6Knobs(value) {
		was, err := setSysctl(path, want)
		if err != nil {
			return restore, err
		}
		previous[path] = was
	}

	return restore, nil
}

// ipv6Knobs maps each knob MLD needs to the value it needs.
func ipv6Knobs(value config.Config) map[string]string {
	target := Target(value)
	knobs := map[string]string{
		filepath.Join(sysctlRoot, target, "forwarding"): "1",
		filepath.Join(sysctlRoot, target, "accept_ra"):  acceptRAWhileForwarding,
	}
	for _, name := range value.LAN.Interfaces {
		knobs[filepath.Join(sysctlRoot, name, "forwarding")] = "1"
	}

	return knobs
}

// IPv6MulticastState reads the knobs EnableIPv6Multicast sets, keyed by the
// interface and knob name the kernel exposes them under. A knob the kernel
// does not expose is left out.
func IPv6MulticastState(value config.Config) map[string]string {
	state := map[string]string{}
	for path := range ipv6Knobs(value) {
		current, err := readSysctl(path)
		if err != nil {
			continue
		}
		state[filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path))] = current
	}

	return state
}

func setSysctl(path, want string) (string, error) {
	was, err := readSysctl(path)
	if err != nil {
		return "", err
	}
	if was == want {
		return was, nil
	}

	return was, writeSysctl(path, want)
}

func readSysctl(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	return strings.TrimSpace(string(content)), nil
}

// A /proc entry is written in place, so it cannot be replaced atomically.
func writeSysctl(path, value string) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(value); err != nil {
		return fmt.Errorf("write %s to %s: %w", value, path, err)
	}

	return nil
}
