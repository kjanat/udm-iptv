package diagnostics

import (
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

// sysfsValueLimit bounds a sysfs attribute read; these files hold one short token.
const sysfsValueLimit = 64

type downstreamStatus struct {
	Interface string `json:"interface"`
	Link      string `json:"link"`
	Snooping  string `json:"snooping"`
	Querier   string `json:"querier"`
}

// Only local kernel state is available without controller credentials. Missing
// data remains unknown: it must never be interpreted as disabled or healthy.
func inspectDownstream(system fs.FS, interfaces []string) []downstreamStatus {
	result := make([]downstreamStatus, 0, len(interfaces))
	read := func(name string, choices map[string]string) string {
		file, err := system.Open(name)
		if err != nil {
			return "not checked"
		}
		defer closeIgnoringError(file)
		data, err := io.ReadAll(io.LimitReader(file, sysfsValueLimit))
		if err == nil {
			if value, ok := choices[strings.TrimSpace(string(data))]; ok {
				return value
			}
		}

		return "not checked"
	}
	for _, name := range interfaces {
		base := path.Join("class/net", name)
		status := downstreamStatus{
			Interface: name,
			Link:      read(path.Join(base, "operstate"), map[string]string{"up": "up", "down": "down", "unknown": "unknown", "lowerlayerdown": "lowerlayerdown", "dormant": "dormant"}),
			Snooping:  read(path.Join(base, "bridge/multicast_snooping"), map[string]string{"0": "disabled", "1": "enabled"}),
			Querier:   read(path.Join(base, "bridge/multicast_querier"), map[string]string{"0": "disabled", "1": "enabled"}),
		}
		result = append(result, status)
	}

	return result
}

func renderDownstream(links []downstreamStatus) string {
	var output strings.Builder
	output.WriteString("\nDownstream checks\n")
	for _, link := range links {
		fmt.Fprintf(&output, "%s: link=%s, snooping=%s, querier=%s\n", link.Interface, link.Link, link.Snooping, link.Querier)
	}
	output.WriteString("Switch firmware/settings: not checked (controller access unavailable).\n")
	output.WriteString("Native UniFi proxy: not checked.\n")
	output.WriteString("TV playback: not checked; router health cannot confirm picture.\n")
	output.WriteString("Freezes? Check switch firmware, snooping, and competing proxies.\n")

	return output.String()
}
