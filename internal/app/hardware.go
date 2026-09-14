package app

import (
	"bufio"
	"net"
	"os"
	"sort"
	"strings"

	"github.com/kjanat/udm-iptv/internal/config"
)

func detectedDefaults() config.Config {
	return withDetectedInterfaces(config.Default())
}

func withDetectedInterfaces(value config.Config) config.Config {
	value = withBoardInterface(value, detectBoard())
	if bridges := bridgeInterfaces(); len(bridges) > 0 {
		value.LAN.Interfaces = bridges
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
	switch strings.ToUpper(strings.TrimSpace(board)) {
	case "UDM", "UDR":
		return "eth4"
	case "UXGPRO":
		return "eth0"
	case "UDR7":
		return "eth3"
	case "UDW":
		return "eth18"
	case "UXG":
		return "eth1"
	case "UDRULT", "UXGB", "UCGMAX":
		return "eth4"
	case "UCGF":
		return "eth6"
	default:
		return "eth8"
	}
}

func detectBoard() string {
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
					closeIgnoringError(file)
					return strings.Trim(strings.TrimSpace(value), `"'`)
				}
			}
		}
		closeIgnoringError(file)
	}
	return ""
}

func bridgeInterfaces() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var result []string
	for _, iface := range interfaces {
		if strings.HasPrefix(iface.Name, "br") {
			result = append(result, iface.Name)
		}
	}
	sort.Strings(result)
	return result
}
