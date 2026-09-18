package service

import (
	"path/filepath"
	"testing"

	"github.com/kjanat/udm-iptv/internal/network"
)

func TestLeaseStateRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "run", "lease.json")
	lease := network.Lease{Action: "bound", Interface: "iptv", Address: "10.207.71.227", Mask: "20", Routers: []string{"10.207.64.1"}, StaticRoutes: []string{"213.75.112.0/21", "10.207.64.1"}, Options: map[string]string{"dns": "195.121.1.34", "lease": "3600"}}
	if err := writeLeaseState(path, lease); err != nil {
		t.Fatal(err)
	}
	state, err := readLeaseState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Received.IsZero() || state.Lease.Address != lease.Address || state.Lease.Options["dns"] != "195.121.1.34" || len(state.Lease.StaticRoutes) != 2 {
		t.Fatalf("lease state = %+v", state)
	}
}
