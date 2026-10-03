package mroute

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

const routerVIFs = `Interface      BytesIn  PktsIn  BytesOut PktsOut Flags Local    Remote
 1 br0               0       0  9800301671 7353563 00000 010AA8C0 00000000
 2 iptv       9800228739 7353500         0       0 00000 E347CF0A 00000000
`

const routerCache = `Group    Origin   Iif     Pkts    Bytes    Wrong Oifs
40FA00E0 D45E79C3 2     182931 76894939        0  1:1  
01BC59E9 010AA8C0 -1         0        0        0
FAFFFFEF 580AA8C0 -1         0        0        0
FAFFFFEF 350AA8C0 -1         0        0        0
FAFFFFEF EE0AA8C0 -1         0        0        0
`

func TestParseRouterTables(t *testing.T) {
	t.Parallel()
	table, err := Parse(strings.NewReader(routerCache), strings.NewReader(routerVIFs))
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Routes) != 1 || table.Unresolved != 4 {
		t.Fatalf("routes = %d, unresolved = %d", len(table.Routes), table.Unresolved)
	}
	want := Route{Group: netip.MustParseAddr("224.0.250.64"), Source: netip.MustParseAddr("195.121.94.212"), Input: "iptv", Outputs: []string{"br0"}, Packets: 182931, Bytes: 76894939}
	if got := table.Routes[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("route = %+v, want %+v", got, want)
	}
	if table.Packets() != 182931 || table.Bytes() != 76894939 {
		t.Fatalf("totals = %d packets, %d bytes", table.Packets(), table.Bytes())
	}
	if key := table.Routes[0].Key(); key != "224.0.250.64<-195.121.94.212@iptv" {
		t.Fatal(key)
	}
}

func TestParseRejectsTruncatedTables(t *testing.T) {
	t.Parallel()
	if _, err := Parse(strings.NewReader(""), strings.NewReader(routerVIFs)); err == nil {
		t.Fatal("empty cache accepted")
	}
	if _, err := Parse(strings.NewReader("Group Origin Iif Pkts Bytes Wrong Oifs\n40FA00E0 D45E79C3 2\n"), strings.NewReader(routerVIFs)); err == nil {
		t.Fatal("short row accepted")
	}
	table, err := Parse(strings.NewReader("Group Origin Iif Pkts Bytes Wrong Oifs\n40FA00E0 D45E79C3 7 1 2 0 9:1\n"), strings.NewReader("Interface\n"))
	if err != nil {
		t.Fatal(err)
	}
	if table.Routes[0].Input != "vif7" || table.Routes[0].Outputs[0] != "vif9" {
		t.Fatalf("unnamed interfaces = %+v", table.Routes[0])
	}
}
