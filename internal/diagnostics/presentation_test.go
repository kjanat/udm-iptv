package diagnostics

import (
	"strings"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/kjanat/udm-iptv/internal/service"
)

type reportFragment struct {
	role ReportRole
	text string
}

func reportFragments(value Snapshot) (string, []reportFragment) {
	var fragments []reportFragment
	text := RenderSnapshotStyled(value, func(role ReportRole, text string) string {
		fragments = append(fragments, reportFragment{role: role, text: text})
		return text
	})
	return text, fragments
}

func requireReportFragment(t *testing.T, fragments []reportFragment, role ReportRole, text string) {
	t.Helper()
	for _, fragment := range fragments {
		if fragment.role == role && fragment.text == text {
			return
		}
	}
	t.Fatalf("missing role %d for %q; fragments: %+v", role, text, fragments)
}

func TestSnapshotPresentationUsesLeaseOutcome(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		applied bool
		role    ReportRole
		text    string
	}{
		{name: "applied", applied: true, role: ReportGood, text: "applied"},
		{name: "rejected", role: ReportBad, text: "not applied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := Snapshot{Lease: &service.LeaseState{Applied: test.applied}}
			_, fragments := reportFragments(value)
			requireReportFragment(t, fragments, test.role, test.text)
			if !test.applied {
				for _, fragment := range fragments {
					if fragment.role == ReportGood && strings.Contains(fragment.text, "applied") {
						t.Fatalf("rejected lease contains a success fragment: %+v", fragment)
					}
				}
			}
		})
	}
}

func TestSnapshotPresentationPreservesFreeText(t *testing.T) {
	t.Parallel()
	const words = "active/running not applied failed enabled disabled healthy up"
	generated := "configuration: " + words + "\nSection:\n  option=" + words
	value := Snapshot{
		Config:      configSummary{Profile: words, DHCPOptionValues: []string{words}},
		ProxyConfig: &generated,
		Lease: &service.LeaseState{
			Applied: false, Failure: "failure detail: " + words,
			Lease: network.Lease{Options: map[string]string{"vendor": words}},
		},
		RecentLogs:  &recentJournal{Events: []Event{{Type: EventLog, Source: "udm-iptv", Log: "log detail: " + words}}},
		NativeProxy: "native detail: " + words,
		Switches:    "switch detail: " + words,
		Playback:    "receiver detail: " + words,
	}
	plain := RenderSnapshot(value)
	text, fragments := reportFragments(value)
	if text != plain || RenderSnapshotStyled(value, nil) != plain {
		t.Fatal("semantic rendering changed plain report contents")
	}
	for _, fragment := range fragments {
		if fragment.role != ReportPlain && strings.Contains(fragment.text, words) {
			t.Errorf("free text received a semantic style: %+v", fragment)
		}
	}
	styled := RenderSnapshotStyled(value, func(role ReportRole, text string) string {
		if role == ReportPlain {
			return text
		}
		return "\x1b[31m" + text + "\x1b[0m"
	})
	stripped := strings.NewReplacer("\x1b[31m", "", "\x1b[0m", "").Replace(styled)
	if stripped != plain {
		t.Fatal("styling modified report contents")
	}
	for _, expected := range []string{generated, "failure detail: " + words, "log detail: " + words, "vendor=" + words} {
		if !strings.Contains(styled, expected) {
			t.Errorf("free text was modified: %q", expected)
		}
	}
}

func TestSnapshotPresentationClassifiesServiceState(t *testing.T) {
	t.Parallel()
	deadline := time.Date(2026, 9, 23, 23, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		status serviceStatus
		role   ReportRole
		text   string
	}{
		{name: "running", status: serviceStatus{ActiveState: "active", SubState: "running"}, role: ReportGood, text: "active/running"},
		{name: "failed", status: serviceStatus{ActiveState: "failed", SubState: "failed"}, role: ReportBad, text: "failed/failed"},
		{name: "stopped", status: serviceStatus{ActiveState: "inactive", SubState: "dead"}, role: ReportWarning, text: "inactive/dead"},
		{name: "paused", status: serviceStatus{ActiveState: "inactive", SubState: "dead", ResumeAt: &deadline}, role: ReportPlain, text: "inactive/dead"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, fragments := reportFragments(Snapshot{Service: test.status})
			requireReportFragment(t, fragments, test.role, test.text)
		})
	}
}

func TestSnapshotPresentationDoesNotColorStaleObservationsAsHealthy(t *testing.T) {
	t.Parallel()
	value := Snapshot{
		Service: serviceStatus{ActiveState: "active", SubState: "running", Errors: map[string]string{"systemd": "read failed"}},
		Network: networkStatus{Target: "iptv", LinkState: "up", Errors: map[string]string{"link": "read failed"}},
	}
	_, fragments := reportFragments(value)
	for _, fragment := range fragments {
		if fragment.role == ReportGood && (strings.Contains(fragment.text, "active/running") || fragment.text == "up") {
			t.Errorf("failed collection presented stale evidence as healthy: %+v", fragment)
		}
	}
}

func TestSnapshotPresentationKeepsDisabledSettingsNeutral(t *testing.T) {
	t.Parallel()
	value := Snapshot{
		Config:     configSummary{DHCP: false, QuickLeave: false, Debug: false, MLDVersion: 0},
		Downstream: []downstreamStatus{{Interface: "br0", Link: "up", Snooping: "disabled", Querier: "disabled"}},
	}
	_, fragments := reportFragments(value)
	for _, fragment := range fragments {
		if fragment.role != ReportGood && fragment.role != ReportWarning && fragment.role != ReportBad {
			continue
		}
		if strings.Contains(fragment.text, "disabled") || strings.Contains(fragment.text, "false") || strings.Contains(fragment.text, "none recorded") {
			t.Errorf("configuration or absent optional lease assigned a health verdict: %+v", fragment)
		}
	}
}

func TestSnapshotPresentationClassifiesVLANObservations(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status string
		active string
		role   ReportRole
		text   string
	}{
		{name: "match", status: "match", role: ReportGood, text: "VLAN configuration matches"},
		{name: "mismatch", status: "mismatch", role: ReportBad, text: "VLAN configuration mismatch"},
		{name: "conflict", status: "wrong-type", role: ReportBad, text: "IPTV interface name conflict"},
		{name: "unavailable", status: "unavailable", role: ReportWarning, text: "VLAN comparison unavailable"},
		{name: "missing while stopped", status: "missing", active: "inactive", role: ReportPlain, text: "Configured IPTV VLAN interface is absent"},
		{name: "missing while running", status: "missing", active: "active", role: ReportBad, text: "Configured IPTV VLAN interface is absent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := Snapshot{
				Service: serviceStatus{ActiveState: test.active},
				Network: networkStatus{VLAN: &vlanCheck{Status: test.status}},
			}
			_, fragments := reportFragments(value)
			requireReportFragment(t, fragments, test.role, test.text)
		})
	}
}

func TestSnapshotPresentationColorsOnlyNATRoutingVerdict(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		routes []string
		role   ReportRole
		text   string
	}{
		{name: "routed", routes: []string{"192.0.2.0/24 via 192.0.2.1"}, role: ReportGood, text: "routed"},
		{name: "unrouted", role: ReportWarning, text: "no route via iptv"},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := []NATEvidence{{Destination: "192.0.2.0/24", Routes: test.routes, Packets: 0, UnmanagedRules: 1}}
			value := Snapshot{Network: networkStatus{Target: "iptv"}, NATEvidence: &evidence}
			_, fragments := reportFragments(value)
			requireReportFragment(t, fragments, test.role, test.text)
			for _, fragment := range fragments {
				checkNATReportFragment(t, fragment, test.routes != nil)
			}
		})
	}
}

func checkNATReportFragment(t *testing.T, fragment reportFragment, routed bool) {
	t.Helper()
	switch fragment.role {
	case ReportGood, ReportBad, ReportWarning:
		if strings.Contains(fragment.text, "192.0.2.") || strings.Contains(fragment.text, "packets") || strings.Contains(fragment.text, "unmanaged") {
			t.Errorf("NAT evidence assigned a verdict beyond observed routing: %+v", fragment)
		}
		if !routed && fragment.role == ReportGood && fragment.text == "routed" {
			t.Error("unrouted destination emitted a discarded success fragment")
		}
	case ReportPlain, ReportHeading, ReportLabel:
		return
	}
}
