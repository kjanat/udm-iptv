package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReadableDiagnosticsIncludeProxySettings(t *testing.T) {
	t.Parallel()
	for _, proxy := range []string{"improxy", "igmpproxy"} {
		for _, enabled := range []bool{true, false} {
			value := Snapshot{Config: configSummary{Proxy: proxy, IGMPVersion: 3, QuickLeave: enabled, Debug: enabled}, Service: serviceStatus{Proxy: proxy}}
			output := RenderSnapshot(value)
			want := "IGMP version: 3, quickleave enabled: false, proxy debug logging: false"
			if enabled {
				want = "IGMP version: 3, quickleave enabled: true, proxy debug logging: true"
			}
			if !strings.Contains(output, want) {
				t.Fatalf("missing proxy settings: %s", output)
			}
			for _, kind := range []string{"initial", "final"} {
				if !strings.Contains(RenderEvent(Event{Type: kind, Snapshot: &value}), want) {
					t.Fatalf("%s capture Snapshot lost proxy settings", kind)
				}
			}
		}
	}
}

func TestRenderSnapshotSeparatesUnreadableCountersFromZero(t *testing.T) {
	t.Parallel()
	idle, none := multicastInfo{}, []string{}
	readable := RenderSnapshot(Snapshot{Multicast: &idle, NAT: &none})
	for _, want := range []string{"Active NAT rules: 0", "Multicast routes: 0 (0 packets)"} {
		if !strings.Contains(readable, want) {
			t.Fatalf("readable snapshot missing %q: %s", want, readable)
		}
	}
	unreadable := RenderSnapshot(Snapshot{})
	for _, want := range []string{"Active NAT rules: unavailable", "Multicast routes: unavailable"} {
		if !strings.Contains(unreadable, want) {
			t.Fatalf("unreadable snapshot missing %q: %s", want, unreadable)
		}
	}
}

func TestSnapshotJSONKeepsUnreadableCountersNull(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"multicast":null`, `"natRules":null`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s: %s", want, data)
		}
	}
}

func TestCaptureSampleSeparatesUnreadableMulticastFromZero(t *testing.T) {
	t.Parallel()
	idle := multicastInfo{}
	if got := RenderEvent(Event{Type: "sample", Snapshot: &Snapshot{Multicast: &idle}}); !strings.Contains(got, "multicast=0") {
		t.Fatalf("idle sample = %s", got)
	}
	if got := RenderEvent(Event{Type: "sample", Snapshot: &Snapshot{}}); !strings.Contains(got, "multicast=unavailable") {
		t.Fatalf("unreadable sample = %s", got)
	}
}
