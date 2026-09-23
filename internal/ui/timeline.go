package ui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kjanat/udm-iptv/internal/diagnostics"
	"github.com/kjanat/udm-iptv/internal/mroute"
)

const (
	// stampLayout is the clock shown on every timeline line, in UTC like the capture.
	stampLayout = "15:04:05"
	// megabit converts bits per second to Mbit/s.
	megabit = 1e6
)

// parseCapture decodes a JSON Lines capture. It returns nothing for a text
// capture, which the viewer then shows as it is.
func parseCapture(content string) []diagnostics.Event {
	var events []diagnostics.Event
	for line := range strings.SplitSeq(content, "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event diagnostics.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		events = append(events, event)
	}

	return events
}

// timeline turns the capture into the lines worth reading: what changed
// between snapshots, and every message, log line and marker in between.
func timeline(events []diagnostics.Event) []string {
	var lines []string
	var previous *diagnostics.Event
	for i := range events {
		event := &events[i]
		stamp := event.Time.UTC().Format(stampLayout)
		switch event.Type {
		case diagnostics.EventInitial, diagnostics.EventSample, diagnostics.EventFinal:
			if event.Snapshot == nil {
				continue
			}
			if previous == nil {
				lines = append(lines, stamp+" "+summarize(event.Snapshot))
			} else {
				for _, change := range changes(previous.Snapshot, event.Snapshot, event.Time.Sub(previous.Time)) {
					lines = append(lines, stamp+" "+change)
				}
			}
			previous = event
		case diagnostics.EventMarker:
			lines = append(lines, stamp+" >>> "+event.Message)
		case diagnostics.EventLog:
			lines = append(lines, stamp+" log "+logSource(event.Source)+event.Log)
		case diagnostics.EventStarted, diagnostics.EventCompleted, diagnostics.EventTimeout, diagnostics.EventError, diagnostics.EventFailed:
			lines = append(lines, stamp+" "+event.Type+": "+event.Message)
		}
	}

	return lines
}

func logSource(source string) string {
	if source == "" {
		return ""
	}

	return source + ": "
}

func summarize(value *diagnostics.Snapshot) string {
	parts := []string{fmt.Sprintf("service %s/%s, proxy %s pid %d", value.Service.ActiveState, value.Service.SubState, value.Service.Proxy, value.Service.ProxyPID)}
	if value.Multicast != nil {
		parts = append(parts, multicastStatus(value))
		for _, route := range scopedMulticastRoutes(value) {
			parts = append(parts, describeRoute(route.Group.String(), route.Source.String(), route.Input, route.Outputs))
		}
	}

	return strings.Join(parts, "; ")
}

func describeRoute(group, source, input string, outputs []string) string {
	return fmt.Sprintf("%s from %s %s -> %s", group, source, input, strings.Join(outputs, ","))
}

// changes lists what differs between two snapshots taken interval apart.
func changes(before, after *diagnostics.Snapshot, interval time.Duration) []string {
	var lines []string
	lines = append(lines, serviceChanges(before, after)...)
	lines = append(lines, networkChanges(before, after)...)
	lines = append(lines, multicastChanges(before, after, interval)...)
	lines = append(lines, membershipChanges(before, after)...)
	if leaseKey(before) != leaseKey(after) && after.Lease != nil {
		lines = append(lines, fmt.Sprintf("lease %s: %s/%s via %s", after.Lease.Lease.Action, after.Lease.Lease.Address, after.Lease.Lease.Mask, strings.Join(after.Lease.Lease.Routers, " ")))
	}

	return lines
}

func serviceChanges(before, after *diagnostics.Snapshot) []string {
	var lines []string
	if before.Service.ActiveState != after.Service.ActiveState || before.Service.SubState != after.Service.SubState {
		lines = append(lines, fmt.Sprintf("service %s/%s -> %s/%s", before.Service.ActiveState, before.Service.SubState, after.Service.ActiveState, after.Service.SubState))
	}
	if before.Service.ProxyPID != after.Service.ProxyPID {
		lines = append(lines, fmt.Sprintf("proxy %s pid %d -> %d", after.Service.Proxy, before.Service.ProxyPID, after.Service.ProxyPID))
	}
	if before.Service.Restarts != after.Service.Restarts {
		lines = append(lines, fmt.Sprintf("systemd restarts %d -> %d", before.Service.Restarts, after.Service.Restarts))
	}

	return lines
}

func networkChanges(before, after *diagnostics.Snapshot) []string {
	var lines []string
	if before.Network.LinkState != after.Network.LinkState {
		lines = append(lines, fmt.Sprintf("%s link %s -> %s", after.Network.Target, before.Network.LinkState, after.Network.LinkState))
	}
	lines = append(lines, setChanges("address", before.Network.Addresses, after.Network.Addresses)...)
	lines = append(lines, setChanges("route", before.Network.Routes, after.Network.Routes)...)
	if before.Network.DefaultRoute != after.Network.DefaultRoute {
		lines = append(lines, fmt.Sprintf("default route on %s: %t -> %t", after.Network.Target, before.Network.DefaultRoute, after.Network.DefaultRoute))
	}

	return lines
}

func setChanges(kind string, before, after []string) []string {
	var lines []string
	for _, value := range after {
		if !slices.Contains(before, value) {
			lines = append(lines, "+ "+kind+" "+value)
		}
	}
	for _, value := range before {
		if !slices.Contains(after, value) {
			lines = append(lines, "- "+kind+" "+value)
		}
	}

	return lines
}

func multicastChanges(before, after *diagnostics.Snapshot, interval time.Duration) []string {
	if before.Multicast == nil || after.Multicast == nil {
		if (before.Multicast == nil) != (after.Multicast == nil) {
			return []string{"multicast table readable: " + strconv.FormatBool(after.Multicast != nil)}
		}

		return nil
	}
	previous := map[string]mroute.Route{}
	for _, route := range scopedMulticastRoutes(before) {
		previous[route.Key()] = route
	}
	lines := routeChanges(previous, scopedMulticastRoutes(after), interval)
	if before.Multicast.Unresolved != after.Multicast.Unresolved {
		lines = append(lines, fmt.Sprintf("system unresolved multicast entries %d -> %d", before.Multicast.Unresolved, after.Multicast.Unresolved))
	}

	return lines
}

func routeChanges(previous map[string]mroute.Route, current []mroute.Route, interval time.Duration) []string {
	var lines []string
	seen := map[string]bool{}
	for _, route := range current {
		seen[route.Key()] = true
		description := describeRoute(route.Group.String(), route.Source.String(), route.Input, route.Outputs)
		earlier, known := previous[route.Key()]
		if !known {
			lines = append(lines, "+ "+description)
		} else {
			lines = append(lines, changedRoute(earlier, route, interval)...)
		}
	}
	for key, route := range previous {
		if !seen[key] {
			lines = append(lines, "- "+describeRoute(route.Group.String(), route.Source.String(), route.Input, route.Outputs))
		}
	}

	return lines
}

func changedRoute(before, after mroute.Route, interval time.Duration) []string {
	description := describeRoute(after.Group.String(), after.Source.String(), after.Input, after.Outputs)
	var lines []string
	if !slices.Equal(slices.Sorted(slices.Values(before.Outputs)), slices.Sorted(slices.Values(after.Outputs))) {
		lines = append(lines, fmt.Sprintf("%s: outputs %s -> %s", description, strings.Join(before.Outputs, ","), strings.Join(after.Outputs, ",")))
	}
	if after.Packets < before.Packets || after.Bytes < before.Bytes {
		return append(lines, fmt.Sprintf("%s: counters reset (packets %d -> %d, bytes %d -> %d)", description, before.Packets, after.Packets, before.Bytes, after.Bytes))
	}
	if after.Packets > before.Packets || after.Bytes > before.Bytes {
		lines = append(lines, fmt.Sprintf("%s: +%d packets (%s)", description, after.Packets-before.Packets, rate(after.Packets-before.Packets, after.Bytes-before.Bytes, interval)))
	}
	return lines
}

// The forwarding cache belongs to the whole kernel. Only entries entering the
// configured IPTV interface and reaching a configured LAN belong to this path;
// they are evidence of forwarding, not proof that a television rendered video.
func scopedMulticastRoutes(value *diagnostics.Snapshot) []mroute.Route {
	if value.Multicast == nil {
		return nil
	}
	if value.Network.Target == "" {
		return value.Multicast.Entries
	}
	var routes []mroute.Route
	for _, route := range value.Multicast.Entries {
		if route.Input != value.Network.Target {
			continue
		}
		if len(value.Config.LANInterfaces) != 0 && !slices.ContainsFunc(route.Outputs, func(output string) bool { return slices.Contains(value.Config.LANInterfaces, output) }) {
			continue
		}
		routes = append(routes, route)
	}
	return routes
}

func multicastStatus(value *diagnostics.Snapshot) string {
	scope := "system multicast (path unknown)"
	if value.Network.Target != "" {
		scope = "multicast on " + value.Network.Target
	}
	routes := scopedMulticastRoutes(value)
	var packets uint64
	for _, route := range routes {
		packets += route.Packets
	}
	return fmt.Sprintf("%s: %d forwarding (%d packets), %d other routes, %d system unresolved", scope, len(routes), packets, len(value.Multicast.Entries)-len(routes), value.Multicast.Unresolved)
}

func rate(packets, bytes uint64, interval time.Duration) string {
	if interval <= 0 {
		return "interval unknown"
	}
	seconds := interval.Seconds()

	return fmt.Sprintf("%.0f pps, %.2f Mbit/s", float64(packets)/seconds, float64(bytes)*8/seconds/megabit)
}

func membershipChanges(before, after *diagnostics.Snapshot) []string {
	if before.Memberships == nil || after.Memberships == nil {
		return nil
	}
	var previous, current []string
	for _, entry := range *before.Memberships {
		previous = append(previous, entry.Bridge+" "+entry.Port+" "+entry.Group)
	}
	for _, entry := range *after.Memberships {
		current = append(current, entry.Bridge+" "+entry.Port+" "+entry.Group)
	}

	return setChanges("member", previous, current)
}

func leaseKey(value *diagnostics.Snapshot) string {
	if value.Lease == nil {
		return ""
	}

	return value.Lease.Received.Format(time.RFC3339Nano)
}

// status is the fixed block above the timeline: the latest state in place.
func status(value *diagnostics.Snapshot) []string {
	if value == nil {
		return []string{"waiting for the first snapshot"}
	}
	lines := []string{
		fmt.Sprintf("service %s/%s  proxy %s pid %d  restarts %d", value.Service.ActiveState, value.Service.SubState, value.Service.Proxy, value.Service.ProxyPID, value.Service.Restarts),
		fmt.Sprintf("%s %s  %s  routes %d  default %t", value.Network.Target, value.Network.LinkState, strings.Join(value.Network.Addresses, " "), len(value.Network.Routes), value.Network.DefaultRoute),
	}
	if value.Multicast != nil {
		lines = append(lines, multicastStatus(value))
	} else {
		lines = append(lines, "multicast table unavailable")
	}
	if value.Lease != nil {
		lines = append(lines, fmt.Sprintf("lease %s %s/%s via %s at %s", value.Lease.Lease.Action, value.Lease.Lease.Address, value.Lease.Lease.Mask, strings.Join(value.Lease.Lease.Routers, " "), value.Lease.Received.UTC().Format(stampLayout)))
	}

	return lines
}
