package diagnostics

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ReportRole describes a report fragment before terminal styling is applied.
// Free-form evidence and configured choices have no success/failure verdict.
type ReportRole uint8

// Report roles distinguish layout from evidence-based verdicts.
const (
	ReportPlain ReportRole = iota
	ReportHeading
	ReportLabel
	ReportGood
	ReportWarning
	ReportBad
)

type reportRenderer struct {
	style func(ReportRole, string) string
}

func (r reportRenderer) text(role ReportRole, text string) string {
	if r.style == nil {
		return text
	}
	return r.style(role, text)
}

func (r reportRenderer) field(label, value string) string {
	return r.text(ReportLabel, label) + ": " + value + "\n"
}

// RenderSnapshotStyled renders directly from snapshot fields. A nil style
// produces the same plain report used in captures and diagnostic attachments.
func RenderSnapshotStyled(value Snapshot, style func(ReportRole, string) string) string {
	r := reportRenderer{style: style}
	var output strings.Builder
	output.WriteString(r.vlan(value.Network.VLAN, value.Service.ActiveState))
	output.WriteString(r.text(ReportHeading, "udm-iptv "+value.Version) + "\n")
	output.WriteString(r.field("Snapshot time", value.Timestamp.Format(time.RFC3339Nano)))
	output.WriteString(r.field("Installation", r.installation(value)))
	output.WriteString(r.configuration(value.Config))
	output.WriteString(r.field("Active NAT rules", r.available(value.NAT != nil, natRuleCount(value.NAT))))
	output.WriteString(r.field("Proxy source ranges", r.sourceRanges(value.Config)))
	output.WriteString(r.field("LAN interfaces", strings.Join(value.Config.LANInterfaces, ", ")))
	output.WriteString(r.service(value.Service))
	output.WriteString(r.proxies(value))
	output.WriteString(r.field("IGMP version", fmt.Sprintf("%d, MLD: %s, quickleave enabled: %t, proxy debug logging: %t",
		value.Config.IGMPVersion, mldText(value.Config.MLDVersion), value.Config.QuickLeave, value.Config.Debug)))
	output.WriteString(r.network(value.Network))
	output.WriteString(r.field("Multicast routes", r.available(value.Multicast != nil, multicastSummary(value.Multicast))))
	output.WriteString(renderMulticast(value.Multicast))
	output.WriteString(renderNAT(value.NAT))
	output.WriteString(r.natEvidence(value.NATEvidence, value.Network.Target))
	output.WriteString(renderIPv6(value.Network))
	if value.Memberships == nil {
		output.WriteString(r.field("Bridge memberships", r.text(ReportWarning, "unavailable")))
	} else {
		output.WriteString(renderMemberships(value.Memberships))
	}
	output.WriteString(r.snapshotLease(value))
	output.WriteString(r.downstream(value))
	output.WriteString(r.systemEvidence(value))
	output.WriteString(renderRecentJournal(value.RecentLogs))
	output.WriteString(r.collectionErrors("snapshot", value.Errors))
	output.WriteString(r.collectionErrors("service", value.Service.Errors))
	output.WriteString(r.collectionErrors("network", value.Network.Errors))
	return output.String()
}

func (r reportRenderer) configuration(value configSummary) string {
	return r.field("Profile", value.Profile) +
		r.field("WAN", fmt.Sprintf("%s, VLAN %d (%s), DHCP: %t", value.WANInterface, value.VLAN, value.IPTVInterface, value.DHCP)) +
		r.field("Custom VLAN MAC", fmt.Sprintf("%t, static address: %t, DHCP options: %t", value.CustomMAC, value.StaticAddress, value.DHCPOptions)) +
		r.field("Configured VLAN MAC", value.MACAddress) +
		r.field("Configured static address", value.StaticCIDR) +
		r.field("Configured DHCP options", fmt.Sprintf("%q", value.DHCPOptionValues)) +
		r.field("Configured proxy", value.Proxy) +
		r.field("DHCP route policy", fallbackText(value.DHCPRoutes)) +
		r.field("NAT destinations", strings.Join(value.NATDestinations, ", "))
}

func (r reportRenderer) available(known bool, text string) string {
	if !known {
		return r.text(ReportWarning, text)
	}
	return text
}

func (r reportRenderer) observed(failures map[string]string, text string, dependencies ...string) string {
	for _, name := range dependencies {
		if failures[name] != "" {
			return r.text(ReportWarning, counterUnavailable)
		}
	}
	return text
}

func (r reportRenderer) installation(value Snapshot) string {
	if value.Service.Errors["package"] != "" {
		return r.text(ReportWarning, counterUnavailable)
	}
	role := ReportPlain
	if value.Service.Package != "" && value.Service.Package != value.Version {
		role = ReportWarning
	}
	return r.text(role, renderInstallation(value.Version, value.Service.Package))
}

func (r reportRenderer) service(value serviceStatus) string {
	role := ReportWarning
	switch value.ActiveState {
	case "active":
		if value.SubState == "running" {
			role = ReportGood
		}
	case "failed":
		role = ReportBad
	case "inactive":
		if value.ResumeAt != nil {
			role = ReportPlain
		}
	}
	if value.Errors["systemd"] != "" {
		role = ReportWarning
	}
	state := r.text(role, fallbackText(value.ActiveState)+"/"+fallbackText(value.SubState))
	return r.field("Service", fmt.Sprintf("%s (%s, restarts: %s)%s", state,
		fallbackText(value.UnitFile), r.observed(value.Errors, strconv.FormatUint(value.Restarts, 10), "systemd"), renderResume(value))) +
		r.field("Service load state", fallbackText(value.LoadState)) +
		r.field("Proxy", fmt.Sprintf("%s (PID %s)", fallbackText(value.Proxy), r.observed(value.Errors, strconv.Itoa(value.ProxyPID), "runtime")))
}

func (r reportRenderer) network(value networkStatus) string {
	role := linkRole(value.LinkState)
	if value.Errors["link"] != "" {
		role = ReportWarning
	}
	return r.field("IPTV interface", fmt.Sprintf("%s (%s, %s IPv4 addresses)", value.Target,
		r.text(role, fallbackText(value.LinkState)),
		r.observed(value.Errors, strconv.Itoa(value.AddressCount), "link", "addresses4"))) +
		r.field("Addresses", r.observed(value.Errors, strings.Join(value.Addresses, ", "), "link", "addresses4")) +
		r.field("Routes", r.observed(value.Errors, strings.Join(value.Routes, ", "), "link", "routes")) +
		r.field("Default route observed on "+value.Target, r.observed(value.Errors, presence(value.DefaultRoute), "link", "routes"))
}

func linkRole(state string) ReportRole {
	switch state {
	case "up":
		return ReportGood
	case linkDown, linkLowerLayerDown:
		return ReportBad
	default:
		return ReportWarning
	}
}

func (r reportRenderer) proxies(value Snapshot) string {
	if value.Proxies == nil {
		return r.field("Proxy availability", r.text(ReportWarning, "not collected"))
	}
	var output strings.Builder
	for _, item := range value.Proxies {
		text, role := "unavailable ("+item.Source+"): "+item.Reason, ReportWarning
		if item.Available {
			text, role = "available ("+item.Source+"): "+item.Path, ReportGood
		}
		output.WriteString(r.field(item.Name+" executable", r.text(role, text)))
		if item.Available {
			output.WriteString(r.field(item.Name+" build", item.Description()))
			output.WriteString(r.field(item.Name+" reported version", fallbackText(item.Version)))
			output.WriteString(r.field(item.Name+" VCS revision", fallbackText(item.Revision)))
			for _, feature := range slices.Sorted(maps.Keys(item.Features)) {
				value := item.Features[feature]
				output.WriteString(r.field(item.Name+" "+feature, value.Status+" ("+value.Evidence+")"))
			}
			for _, message := range item.MetadataErrors {
				output.WriteString(r.field(item.Name+" metadata", r.text(ReportWarning, message)))
			}
		}
	}
	return output.String()
}
