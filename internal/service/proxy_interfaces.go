package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/mroute"
	"github.com/kjanat/udm-iptv/internal/network"
)

const proxyInterfaceTimeout = 5 * time.Second

var errDownstreamNotReady = errors.New("downstream is not ready")

// ProxyInterfaces records actual kernel registration, not just the generated
// config or process liveness. Raw tables are retained for diagnostic evidence.
type ProxyInterfaces struct {
	ConfigError string             `json:"configError,omitempty"`
	Expected    []string           `json:"expected"`
	IPv4        mroute.Interfaces  `json:"ipv4"`
	IPv6        *mroute.Interfaces `json:"ipv6,omitempty"`
}

// InspectProxyInterfaces observes every configured multicast interface.
func InspectProxyInterfaces(value config.Config) ProxyInterfaces {
	result := ProxyInterfaces{
		Expected: append([]string{network.Target(value)}, value.LAN.Interfaces...),
		IPv4:     mroute.ReadInterfaces(false),
	}
	result.ConfigError = proxyConfigurationError(value)
	if value.Proxy.MLDVersion != 0 {
		table := mroute.ReadInterfaces(true)
		result.IPv6 = &table
	}
	return result
}

// Check refuses partial registration, including unreadable kernel evidence.
func (state ProxyInterfaces) Check() error {
	err := state.IPv4.Check(state.Expected)
	if err != nil {
		err = fmt.Errorf("IPv4: %w", err)
	}
	if state.IPv6 != nil {
		if cause := state.IPv6.Check(state.Expected); cause != nil {
			err = errors.Join(err, fmt.Errorf("IPv6: %w", cause))
		}
	}
	if state.ConfigError != "" {
		err = errors.Join(err, fmt.Errorf("%w: %s", errProxyConfiguration, state.ConfigError))
	}
	return err
}

var errProxyConfiguration = errors.New("running proxy configuration differs from saved settings")

func proxyConfigurationError(value config.Config) string {
	expected, err := renderProxyConfig(value)
	if err != nil {
		return err.Error()
	}
	actual, err := ReadProxyConfig()
	if err != nil {
		return err.Error()
	}
	if strings.TrimSpace(actual) != strings.TrimSpace(expected) {
		return "generated proxy configuration does not match the expected configuration; restart to apply settings"
	}
	return ""
}

// waitDownstreams runs before starting the proxy: IMProxy permanently skips
// a downstream that has no IPv4 address when it initializes its interfaces.
func waitDownstreams(ctx context.Context, names []string) error {
	return waitProxyCheck(ctx, proxyInterfaceTimeout, func() error {
		return checkDownstreams(names, net.InterfaceByName)
	})
}

func checkDownstreams(names []string, lookup func(string) (*net.Interface, error)) error {
	for _, name := range names {
		link, err := lookup(name)
		if err != nil {
			return fmt.Errorf("%w: %s: %w", errDownstreamNotReady, name, err)
		}
		addresses, err := link.Addrs()
		if err != nil {
			return fmt.Errorf("read %s addresses: %w", name, err)
		}
		if err := checkDownstreamAddresses(name, link.Flags, addresses); err != nil {
			return err
		}
	}
	return nil
}

func checkDownstreamAddresses(name string, flags net.Flags, addresses []net.Addr) error {
	if flags&net.FlagUp == 0 {
		return fmt.Errorf("%w: %s is down", errDownstreamNotReady, name)
	}
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.To4() != nil && !ip.IsUnspecified() {
			return nil
		}
	}
	return fmt.Errorf("%w: %s has no IPv4 address", errDownstreamNotReady, name)
}

func waitProxyCheck(parent context.Context, timeout time.Duration, check func() error) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for proxy interfaces: %w", err)
		}
		err := check()
		if err == nil {
			return nil
		}
		if cause := waitOrCancel(ctx, healthCheckInterval); cause != nil {
			return errors.Join(err, cause)
		}
	}
}
