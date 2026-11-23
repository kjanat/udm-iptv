package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/mroute"
	"github.com/kjanat/udm-iptv/internal/service"
)

func TestSnapshotExposesPartialProxyAndKernelEvidence(t *testing.T) {
	raw := "Interface BytesIn PktsIn BytesOut PktsOut Flags Local Remote\n0 iptv 0 0 0 0 00000 010200C0 00000000\n1 br0 0 0 0 0 00000 010AA8C0 00000000\n"
	value := Snapshot{ProxyInterfaces: &service.ProxyInterfaces{
		Expected: []string{"iptv", "br0", "br2"},
		IPv4:     mroute.Interfaces{Names: []string{"iptv", "br0"}, Raw: raw},
	}}
	value.Service.ActiveState, value.Service.SubState = "active", "running"
	text := RenderSnapshot(value)
	for _, want := range []string{"WARNING: IPv4: multicast interface not registered: br2", "Kernel IPv4 multicast VIF table:\n" + raw} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in report: %s", want, text)
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ProxyInterfaces.IPv4.Raw != raw || decoded.ProxyInterfaces.Check() == nil {
		t.Fatalf("structured evidence lost: %+v", decoded.ProxyInterfaces)
	}
	decoded.ProxyInterfaces.IPv4.Names = append(decoded.ProxyInterfaces.IPv4.Names, "br2")
	if text := RenderSnapshot(decoded); !strings.Contains(text, "all configured interfaces registered (not a playback check)") {
		t.Fatal(text)
	}
}

func TestSnapshotDoesNotInventRegistrationEvidence(t *testing.T) {
	for _, value := range []Snapshot{
		{},
		{ProxyInterfaces: &service.ProxyInterfaces{Expected: []string{"br2"}, IPv4: mroute.Interfaces{Error: "permission denied"}}},
	} {
		text := RenderSnapshot(value)
		if strings.Contains(text, "all configured interfaces registered") {
			t.Fatal(text)
		}
	}
}
