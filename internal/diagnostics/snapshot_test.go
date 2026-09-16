package diagnostics

import (
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
