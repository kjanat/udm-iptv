package telemetry

import (
	"testing"

	"github.com/kjanat/udm-iptv/internal/config/configtest"
	"github.com/kjanat/udm-iptv/internal/proxyinventory"
)

func TestProxyInventorySupportsAggregationAndResearch(t *testing.T) {
	r, transport := researchReporter(t)
	r.proxies = proxyinventory.Inventory{
		{Name: "improxy", Available: true, Source: "system", Version: "0.3", BuildID: "123abc"},
		{Name: "igmpproxy", Available: true, Source: "offline", Version: "0.4"},
	}
	r.SetMetadata("UDMPRO", "5.1.33", "firmware", "id", "improxy", "custom")
	tags := r.eventTags()
	for name, want := range map[string]string{"proxy": "improxy", "proxy.igmpproxy.available": "true", "proxy.igmpproxy.source": "offline", "proxy.improxy.version": "0.3", "proxy.improxy.build_id": "123abc"} {
		if tags[name] != want {
			t.Errorf("tag %s = %q, want %q", name, tags[name], want)
		}
	}
	if err := r.RecordConfiguration(t.Context(), configtest.Custom(), true, nil); err != nil {
		t.Fatal(err)
	}
	report := reportAt(t, transport, 0)
	if len(report.Proxies) != 2 || report.Proxies.Find("igmpproxy").Version != "0.4" {
		t.Fatal("configuration report omitted alternative proxy inventory")
	}
}
