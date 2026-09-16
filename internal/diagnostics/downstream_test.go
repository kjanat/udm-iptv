package diagnostics

import (
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
	if links[0].Link != "up" || links[0].Snooping != "enabled" || links[0].Querier != "disabled" {
		t.Fatalf("local bridge state: %+v", links[0])
	}
	for _, link := range links[1:] {
		if link.Snooping != "not checked" || link.Querier != "not checked" || link.Link != "not checked" {
			t.Fatalf("unknown became healthy: %+v", link)
		}
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
