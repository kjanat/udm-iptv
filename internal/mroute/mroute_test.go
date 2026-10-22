package mroute

import (
	"errors"
	"io"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestReadTablesEnforcesBothSizeLimits(t *testing.T) {
	t.Parallel()
	for _, tableName := range []string{"cache", "vif"} {
		for _, extra := range []int{0, 1} {
			t.Run(tableName+"/extra="+strconv.Itoa(extra), func(t *testing.T) {
				cache, vif := routerCache, routerVIFs
				if tableName == "cache" {
					cache += strings.Repeat("\n", readLimit+extra-len(cache))
				} else {
					vif += strings.Repeat("\n", readLimit+extra-len(vif))
				}
				table, err := readTables(strings.NewReader(cache), strings.NewReader(vif))
				if extra > 0 {
					if !errors.Is(err, errTruncated) || len(table.Routes) != 0 {
						t.Fatalf("oversized %s returned partial success: %+v, %v", tableName, table, err)
					}
					return
				}
				if err != nil || len(table.Routes) != 1 || table.Routes[0].Input != "iptv" {
					t.Fatalf("exact-limit %s rejected: %+v, %v", tableName, table, err)
				}
			})
		}
	}
}

type failingTableReader struct{}

func (failingTableReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestReadTablesRetainsReadErrors(t *testing.T) {
	t.Parallel()
	for _, cacheFails := range []bool{false, true} {
		var cache, vif io.Reader = strings.NewReader(routerCache), strings.NewReader(routerVIFs)
		wantPath := vifPath
		if cacheFails {
			cache, wantPath = failingTableReader{}, cachePath
		} else {
			vif = failingTableReader{}
		}
		table, err := readTables(cache, vif)
		if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), wantPath) || len(table.Routes) != 0 {
			t.Fatalf("read failure lost: %+v, %v", table, err)
		}
	}
}

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
