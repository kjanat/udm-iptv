package device

import (
	"net"
	"os"
	"sort"
	"strings"
)

// Port describes locally observed interface state.
type Port struct {
	Name, Description string
	Addresses         []string
	AddressesKnown    bool
}

func Ports() []Port {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	route := defaultRouteInterfaceFromSystem()
	candidates := wanInterfacesForBoard(Board())
	var ports []Port
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		description := "link status unknown"
		if data, err := os.ReadFile("/sys/class/net/" + iface.Name + "/carrier"); err == nil {
			switch strings.TrimSpace(string(data)) {
			case "1":
				description = "connected"
			case "0":
				description = "disconnected"
			}
		}
		if iface.Name == route {
			description += ", Internet route"
		}
		if slicesContain(candidates, iface.Name) {
			description += ", WAN candidate"
		}
		port := Port{Name: iface.Name, Description: description}
		addresses, err := iface.Addrs()
		if err == nil {
			port.AddressesKnown = true
			for _, address := range addresses {
				port.Addresses = append(port.Addresses, address.String())
			}
		}
		ports = append(ports, port)
	}
	sort.SliceStable(ports, func(i, j int) bool {
		rank := func(name string) int {
			if name == route {
				return 0
			}
			if slicesContain(candidates, name) {
				return 1
			}

			return 2
		}

		return rank(ports[i].Name) < rank(ports[j].Name)
	})

	return ports
}
