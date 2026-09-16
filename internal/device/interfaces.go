package device

import (
	"bufio"
	"io"
	"net"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/kjanat/udm-iptv/internal/config"
)

// Defaults returns config.Default with WAN and LAN interfaces detected for this board.
func Defaults() config.Config {
	return WithInterfaces(config.Default())
}

// WithInterfaces replaces value's WAN and LAN interfaces with ones detected for this board.
func WithInterfaces(value config.Config) config.Config {
	board := Board()
	value = withBoardInterface(value, board)
	if downstream := downstreamInterfaces(board); len(downstream) > 0 {
		value.LAN.Interfaces = downstream
	}

	return value
}

func withBoardInterface(value config.Config, board string) config.Config {
	detectedWAN := wanInterfaceForBoard(board)
	defaultWAN := config.Default().WAN.Interface
	switch {
	case value.WAN.Interface == defaultWAN:
		value.WAN.Interface = detectedWAN
	case strings.HasPrefix(value.WAN.Interface, defaultWAN+"."):
		value.WAN.Interface = detectedWAN + strings.TrimPrefix(value.WAN.Interface, defaultWAN)
	}

	return value
}

func wanInterfaceForBoard(board string) string {
	candidates := wanInterfacesForBoard(board)
	if route := defaultRouteInterfaceFromSystem(); slicesContain(candidates, route) {
		return route
	}
	for _, candidate := range candidates {
		carrier, err := os.ReadFile("/sys/class/net/" + candidate + "/carrier")
		if err == nil && strings.TrimSpace(string(carrier)) == "1" {
			return candidate
		}
	}

	return candidates[0]
}

// wanEth4 is the WAN candidate name shared by several board families below,
// each with an otherwise unrelated kernel interface layout.
const wanEth4 = "eth4"

func wanInterfacesForBoard(board string) []string {
	switch strings.ToUpper(strings.TrimSpace(board)) {
	case "UDM", "UDR":
		return []string{wanEth4}
	case "UXGPRO":
		return []string{"eth0", "eth2"}
	case "UDR7":
		return []string{"eth3", wanEth4, "eth2"}
	case "UDW":
		return []string{"eth18", "eth19"}
	case "UXG":
		return []string{"eth1"}
	case "UDRULT", "UXGB", "UCGMAX":
		return []string{wanEth4, "eth3"}
	case "UCGF":
		return []string{"eth6", wanEth4}
	case "UDMPRO", "UDMPROSE", "UDMSE", "UDMPROMAX", "UDMEA4C":
		return []string{"eth8", "eth9"}
	default:
		return []string{"eth8"}
	}
}

func defaultRouteInterfaceFromSystem() string {
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()

	return defaultRouteInterface(file)
}

func defaultRouteInterface(reader io.Reader) string {
	scanner := bufio.NewScanner(reader)
	selected := ""
	selectedMetric := int64(^uint64(0) >> 1)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 || fields[1] != "00000000" {
			continue
		}
		flags, flagErr := strconv.ParseUint(fields[3], 16, 64)
		metric, metricErr := strconv.ParseInt(fields[6], 10, 64)
		if flagErr != nil || metricErr != nil || flags&1 == 0 || metric >= selectedMetric {
			continue
		}
		selected, selectedMetric = fields[0], metric
	}
	if scanner.Err() != nil {
		// An incomplete table may omit a preferred route. Let WAN discovery
		// fall back to carrier detection instead of trusting a partial result.
		return ""
	}

	return selected
}

// Board returns this device's short board name, or "" when undetectable.
func Board() string {
	for _, source := range []struct {
		path string
		keys []string
	}{
		{path: "/etc/board.info", keys: []string{"board.shortname"}},
		{path: "/proc/ubnthal/system.info", keys: []string{"shortname"}},
	} {
		file, err := os.Open(source.path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			key, value, found := strings.Cut(scanner.Text(), "=")
			if !found {
				continue
			}
			for _, wanted := range source.keys {
				if strings.TrimSpace(key) == wanted {
					_ = file.Close()

					return strings.Trim(strings.TrimSpace(value), `"'`)
				}
			}
		}
		_ = file.Close()
	}

	return ""
}

func downstreamInterfaces(board string) []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	return selectDownstreamInterfaces(board, interfaces)
}

func selectDownstreamInterfaces(board string, interfaces []net.Interface) []string {
	var result []string
	for _, iface := range interfaces {
		if strings.HasPrefix(iface.Name, "br") || (strings.EqualFold(strings.TrimSpace(board), "UXG") && strings.HasPrefix(iface.Name, "eth0.")) {
			result = append(result, iface.Name)
		}
	}
	sort.Strings(result)

	return result
}

func slicesContain(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}
