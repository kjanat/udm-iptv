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

const (
	rankRoute = iota
	rankCandidate
	rankOther
)

// Ports returns a list of locally observed network interfaces,
// sorted by relevance to WAN connectivity.
func Ports() []Port {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	ethernet := listEthernet()
	internet := defaultRouteInterfaceFromSystem()
	route := walkToEthernet(internet, sysLower)
	candidates := wanCandidateList(ethernet, skippedLinks(ethernet), wanHints(Board()))
	var ports []Port
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		port := Port{Name: iface.Name, Description: describePort(iface.Name, internet, route, candidates)}
		port.Addresses, port.AddressesKnown = interfaceAddresses(iface)
		ports = append(ports, port)
	}
	sort.SliceStable(ports, func(i, j int) bool {
		return wanRank(ports[i].Name, internet, route, candidates) < wanRank(ports[j].Name, internet, route, candidates)
	})

	return ports
}

func carrierState(name string) string {
	data, err := os.ReadFile("/sys/class/net/" + name + "/carrier")
	if err != nil {
		return "link status unknown"
	}
	switch strings.TrimSpace(string(data)) {
	case "1":
		return "connected"
	case "0":
		return "disconnected"
	default:
		return "link status unknown"
	}
}

func describePort(name, internet, route string, candidates []string) string {
	description := carrierState(name)
	if name == internet || name == route {
		description += ", Internet route"
	}
	if slicesContain(candidates, name) {
		description += ", WAN candidate"
	}

	return description
}

func interfaceAddresses(iface net.Interface) ([]string, bool) {
	addresses, err := iface.Addrs()
	if err != nil {
		return nil, false
	}
	var result []string
	for _, address := range addresses {
		result = append(result, address.String())
	}

	return result, true
}

func wanRank(name, internet, route string, candidates []string) int {
	switch {
	case name == internet || name == route:
		return rankRoute
	case slicesContain(candidates, name):
		return rankCandidate
	default:
		return rankOther
	}
}
