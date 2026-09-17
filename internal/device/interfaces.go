package device

import (
	"bufio"
	"io"
	"net"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/kjanat/udm-iptv/internal/config"
)

// Defaults seeds a fresh installation, before the wizard asks for a provider,
// with the KPN profile and the WAN and LAN interfaces detected for this board.
func Defaults() config.Config {
	return WithInterfaces(config.DefaultKPN())
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
	return rewriteWAN(value, wanInterfaceForBoard(board))
}

func rewriteWAN(value config.Config, detected string) config.Config {
	defaultWAN := config.Default().WAN.Interface
	switch {
	case value.WAN.Interface == defaultWAN:
		value.WAN.Interface = detected
	case strings.HasPrefix(value.WAN.Interface, defaultWAN+"."):
		value.WAN.Interface = detected + strings.TrimPrefix(value.WAN.Interface, defaultWAN)
	}

	return value
}

func wanInterfaceForBoard(board string) string {
	ethernet := listEthernet()
	carrier := map[string]bool{}
	for _, name := range ethernet {
		carrier[name] = carrierUp(name)
	}

	return selectWAN(walkToEthernet(defaultRouteInterfaceFromSystem(), sysLower), ethernet, skippedLinks(ethernet), carrier, wanHints(board))
}

func wanHints(board string) []string {
	return wanByBoard[strings.ToUpper(strings.TrimSpace(board))]
}

func ethernetIndex(name string) (int, bool) {
	if !strings.HasPrefix(name, "eth") {
		return 0, false
	}
	index, err := strconv.Atoi(name[len("eth"):])
	if err != nil || index < 0 {
		return 0, false
	}

	return index, true
}

func walkToEthernet(name string, lower func(string) string) string {
	seen := map[string]bool{}
	for name != "" && !seen[name] {
		seen[name] = true
		if _, ok := ethernetIndex(name); ok {
			return name
		}
		parent, extra, cut := strings.Cut(name, ".")
		if cut && extra != "" {
			name = parent

			continue
		}
		if lower == nil {
			return ""
		}
		name = lower(name)
	}

	return ""
}

func sysLower(name string) string {
	if name == "" || strings.Contains(name, "/") {
		return ""
	}
	entries, err := os.ReadDir("/sys/class/net/" + name)
	if err == nil {
		for _, entry := range entries {
			if lower, ok := strings.CutPrefix(entry.Name(), "lower_"); ok && lower != "" && !strings.Contains(lower, "/") {
				return lower
			}
		}
	}

	return iflinkParent(name)
}

func iflinkParent(name string) string {
	iflink, err := os.ReadFile("/sys/class/net/" + name + "/iflink")
	if err != nil {
		return ""
	}
	index := strings.TrimSpace(string(iflink))
	self, err := os.ReadFile("/sys/class/net/" + name + "/ifindex")
	if err == nil && strings.TrimSpace(string(self)) == index {
		return ""
	}
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		data, err := os.ReadFile("/sys/class/net/" + entry.Name() + "/ifindex")
		if err == nil && strings.TrimSpace(string(data)) == index {
			return entry.Name()
		}
	}

	return ""
}

func switchPort(name string) bool {
	if _, err := os.Stat("/sys/class/net/" + name + "/dsa"); err == nil {
		return true
	}
	target, err := os.Readlink("/sys/class/net/" + name + "/master")
	if err != nil {
		return false
	}

	return strings.HasPrefix(path.Base(target), "switch")
}

func skippedLinks(ethernet []string) map[string]bool {
	skip := bridgedNames()
	if skip == nil {
		skip = map[string]bool{}
	}
	for _, name := range ethernet {
		if switchPort(name) {
			skip[name] = true
		}
	}

	return skip
}

func listEthernet() []string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if _, ok := ethernetIndex(entry.Name()); ok {
			names = append(names, entry.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool {
		left, _ := ethernetIndex(names[i])
		right, _ := ethernetIndex(names[j])

		return left < right
	})

	return names
}

func bridgedNames() map[string]bool {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	bridged := map[string]bool{}
	for _, entry := range entries {
		members, err := os.ReadDir("/sys/class/net/" + entry.Name() + "/brif")
		if err != nil {
			continue
		}
		for _, member := range members {
			bridged[member.Name()] = true
		}
	}

	return bridged
}

func carrierUp(name string) bool {
	data, err := os.ReadFile("/sys/class/net/" + name + "/carrier")

	return err == nil && strings.TrimSpace(string(data)) == "1"
}

func ethernetPool(ethernet []string, skip map[string]bool) []string {
	var kept, all []string
	for _, name := range ethernet {
		if _, ok := ethernetIndex(name); !ok {
			continue
		}
		all = append(all, name)
		if !skip[name] {
			kept = append(kept, name)
		}
	}
	if len(kept) > 0 {
		return kept
	}

	return all
}

func prependHints(pool, hints []string) []string {
	seen := map[string]bool{}
	var candidates []string
	for _, name := range hints {
		if slices.Contains(pool, name) && !seen[name] {
			candidates = append(candidates, name)
			seen[name] = true
		}
	}
	for _, name := range pool {
		if !seen[name] {
			candidates = append(candidates, name)
			seen[name] = true
		}
	}

	return candidates
}

func wanCandidateList(ethernet []string, skip map[string]bool, hints []string) []string {
	return prependHints(ethernetPool(ethernet, skip), hints)
}

func selectWAN(route string, ethernet []string, skip, carrier map[string]bool, hints []string) string {
	route = walkToEthernet(route, nil)
	candidates := wanCandidateList(ethernet, skip, hints)
	if len(candidates) == 0 {
		return wanEth8
	}
	if route != "" && slices.Contains(candidates, route) {
		return route
	}
	for _, name := range candidates {
		if carrier[name] {
			return name
		}
	}

	return candidates[0]
}

// wanEth4 is the WAN candidate name shared by several board families below,
// each with an otherwise unrelated kernel interface layout.
const (
	wanEth0  = "eth0"
	wanEth1  = "eth1"
	wanEth2  = "eth2"
	wanEth3  = "eth3"
	wanEth4  = "eth4"
	wanEth5  = "eth5"
	wanEth6  = "eth6"
	wanEth7  = "eth7"
	wanEth8  = "eth8"
	wanEth9  = "eth9"
	wanEth10 = "eth10"
	wanEth11 = "eth11"
	wanEth12 = "eth12"
	wanEth13 = "eth13"
	wanEth14 = "eth14"
	wanEth15 = "eth15"
	wanEth16 = "eth16"
	wanEth17 = "eth17"
	wanEth18 = "eth18"
	wanEth19 = "eth19"
)

var wanByBoard = map[string][]string{
	"UDM":       {wanEth4},
	"UDR":       {wanEth4},
	"UXGPRO":    {wanEth0, wanEth2},
	"UDR7":      {wanEth3, wanEth4, wanEth2},
	"UDW":       {wanEth18, wanEth19},
	"UXG":       {wanEth1},
	"UDRULT":    {wanEth4, wanEth3},
	"UXGB":      {wanEth4, wanEth3},
	"UCGMAX":    {wanEth4, wanEth3},
	"UCGF":      {wanEth6, wanEth4},
	"UDMPRO":    {wanEth8, wanEth9},
	"UDMPROSE":  {wanEth8, wanEth9},
	"UDMSE":     {wanEth8, wanEth9},
	"UDMPROMAX": {wanEth8, wanEth9},
	"UDMEA4C":   {wanEth8, wanEth9},
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
	return hardwareFromFiles().Board
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
