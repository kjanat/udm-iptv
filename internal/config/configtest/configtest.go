// Package configtest builds configurations for a console with one WAN port
// and one LAN bridge, for tests that need a configuration a service could run.
package configtest

import "github.com/kjanat/udm-iptv/internal/config"

const (
	wanInterface = "eth8"
	lanInterface = "br0"
)

// WithPorts gives value the console's WAN port and LAN bridge.
func WithPorts(value config.Config) config.Config {
	value.WAN.Interface = wanInterface
	value.LAN.Interfaces = []string{lanInterface}

	return value
}

// Custom is the custom profile on the console.
func Custom() config.Config {
	return WithPorts(config.Default())
}

// KPN is the KPN profile on the console.
func KPN() config.Config {
	return WithPorts(config.DefaultKPN())
}
