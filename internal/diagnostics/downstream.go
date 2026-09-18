package diagnostics

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path"
	"strconv"
	"strings"
	"unicode"
)

const sysfsValueLimit = 64

var linkOperStates = map[string]string{"up": "up", "down": "down", "unknown": "unknown", "lowerlayerdown": "lowerlayerdown", "dormant": "dormant"}

type downstreamStatus struct {
	Interface string `json:"interface"`
	Link      string `json:"link"`
	Snooping  string `json:"snooping"`
	Querier   string `json:"querier"`
}

func inspectDownstream(system fs.FS, interfaces []string) []downstreamStatus {
	result := make([]downstreamStatus, 0, len(interfaces))
	flags := map[string]string{"0": "disabled", "1": "enabled"}
	for _, name := range interfaces {
		base := path.Join("class/net", name)
		status := downstreamStatus{
			Interface: name,
			Link:      readSysfsToken(system, path.Join(base, "operstate"), linkOperStates),
			Snooping:  readSysfsToken(system, path.Join(base, "bridge/multicast_snooping"), flags),
			Querier:   readSysfsToken(system, path.Join(base, "bridge/multicast_querier"), flags),
		}
		result = append(result, status)
	}

	return result
}

func renderDownstream(value Snapshot) string {
	var output strings.Builder
	output.WriteString("\nDownstream checks\n")
	for _, link := range value.Downstream {
		output.WriteString(formatDownstream(link) + "\n")
	}
	fmt.Fprintf(&output, "Switch: %s\n", fallbackText(value.Switches))
	fmt.Fprintf(&output, "Native UniFi proxy: %s\n", fallbackText(value.NativeProxy))
	fmt.Fprintf(&output, "Receivers: %s\n", fallbackText(value.Playback))

	return output.String()
}

func inspectSwitch(system fs.FS, firmware string) string {
	ports, err := switchPorts(system)
	state := "no local switch interfaces"
	switch {
	case err != nil:
		state = "local switch interfaces unavailable"
	case len(ports) > 0:
		state = strings.Join(ports, ", ")
	}
	if firmware == "" {
		return state
	}

	return "firmware " + firmware + ", " + state
}

func switchPorts(system fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(system, "class/net")
	if err != nil {
		return nil, fmt.Errorf("read class/net: %w", err)
	}
	var ports []string
	for _, entry := range entries {
		name := entry.Name()
		if !isSwitchDevice(name) {
			continue
		}
		state := readSysfsToken(system, path.Join("class/net", name, "operstate"), linkOperStates)
		if state == "" {
			state = "absent"
		}
		ports = append(ports, name+" "+state)
	}

	return ports, nil
}

func isSwitchDevice(name string) bool {
	rest, ok := strings.CutPrefix(name, "switch")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if !unicode.IsDigit(r) {
			return false
		}
	}

	return true
}

func formatNativeProxy(unitLoaded bool, activeState string, extra []int, scanned error) string {
	unit := "igmpproxy.service not loaded"
	if unitLoaded {
		if activeState == "" {
			activeState = "unknown"
		}
		unit = "igmpproxy.service " + activeState
	}
	if scanned != nil {
		return unit + ", extra proxy processes unavailable"
	}
	if len(extra) == 0 {
		return unit + ", no extra proxy processes"
	}
	parts := make([]string, 0, len(extra))
	for _, pid := range extra {
		parts = append(parts, strconv.Itoa(pid))
	}

	return unit + ", extra proxy pids " + strings.Join(parts, " ")
}

func extraProxyPIDs(ours int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	var extra []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == ours {
			continue
		}
		comm, err := os.ReadFile("/proc/" + entry.Name() + "/comm")
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(comm)) {
		case "improxy", "igmpproxy":
			extra = append(extra, pid)
		}
	}

	return extra, nil
}

func formatReceivers(usage *multicastInfo, groups *int) string {
	routes := "multicast routes unavailable"
	if usage != nil {
		routes = fmt.Sprintf("%d multicast routes (%d packets)", usage.Routes, usage.Packets)
	}
	membership := "IGMP groups on LAN unavailable"
	if groups != nil {
		membership = strconv.Itoa(*groups) + " IGMP groups on LAN"
	}

	return routes + ", " + membership
}

func countLANIGMPGroups(table string, lan []string) int {
	wanted := make(map[string]bool, len(lan))
	for _, name := range lan {
		wanted[name] = true
	}
	linkLocal := netip.MustParsePrefix("224.0.0.0/24")
	count := 0
	current := ""
	for line := range strings.SplitSeq(strings.TrimSpace(table), "\n") {
		fields := strings.Fields(line)
		if name, ok := igmpDevice(fields); ok {
			current = name
			continue
		}
		if !wanted[current] || len(fields) == 0 {
			continue
		}
		group, ok := igmpGroup(fields[0])
		if !ok || linkLocal.Contains(group) {
			continue
		}
		count++
	}

	return count
}

func igmpDevice(fields []string) (string, bool) {
	for i, field := range fields {
		if field == ":" && i > 0 {
			return fields[i-1], true
		}
		if strings.HasSuffix(field, ":") && field != ":" {
			return strings.TrimSuffix(field, ":"), true
		}
	}

	return "", false
}

func igmpGroup(token string) (netip.Addr, bool) {
	value, err := strconv.ParseUint(token, 16, 32)
	if err != nil {
		return netip.Addr{}, false
	}
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], uint32(value))

	return netip.AddrFrom4(raw), true
}

func formatDownstream(link downstreamStatus) string {
	var parts []string
	if link.Link != "" {
		parts = append(parts, "link="+link.Link)
	}
	if link.Snooping != "" {
		parts = append(parts, "snooping="+link.Snooping)
	}
	if link.Querier != "" {
		parts = append(parts, "querier="+link.Querier)
	}
	if len(parts) == 0 {
		return link.Interface + ": no sysfs"
	}

	return link.Interface + ": " + strings.Join(parts, ", ")
}

func readSysfsToken(system fs.FS, name string, choices map[string]string) string {
	file, err := system.Open(name)
	if err != nil {
		return ""
	}
	defer closeIgnoringError(file)
	data, err := io.ReadAll(io.LimitReader(file, sysfsValueLimit))
	if err != nil {
		return ""
	}
	token := strings.TrimSpace(string(data))
	if value, ok := choices[token]; ok {
		return value
	}
	if token == "" {
		return ""
	}

	return "unrecognized"
}
