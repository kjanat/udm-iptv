package diagnostics

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDownstreamMissingDataIsNotHealthy(t *testing.T) {
	system := fstest.MapFS{
		"class/net/br0/operstate":                 {Data: []byte("up\n")},
		"class/net/br0/bridge/multicast_snooping": {Data: []byte("1\n")},
		"class/net/br0/bridge/multicast_querier":  {Data: []byte("0\n")},
		"class/net/br1/bridge/multicast_snooping": {Data: []byte("secret-unexpected-value")},
	}
	links := inspectDownstream(system, []string{"br0", "br1", "eth0.10"})
	want := []downstreamStatus{
		{Interface: "br0", Link: "up", Snooping: "enabled", Querier: "disabled"},
		{Interface: "br1", Link: notChecked, Snooping: notChecked, Querier: notChecked},
		{Interface: "eth0.10", Link: notChecked, Snooping: notChecked, Querier: notChecked},
	}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("unknown became healthy: %+v", links)
	}
	text := RenderSnapshot(Snapshot{Downstream: links})
	for _, required := range []string{"Switch firmware/settings: not checked", "Native UniFi proxy: not checked", "TV playback: not checked"} {
		if !strings.Contains(text, required) {
			t.Errorf("missing %s", required)
		}
	}
	if strings.Contains(text, "secret") {
		t.Fatal("unexpected kernel data leaked")
	}
}
