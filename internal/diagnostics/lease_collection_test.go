package diagnostics

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/service"
)

func TestCollectLeaseDistinguishesOptionalAbsenceFromFailure(t *testing.T) {
	t.Parallel()
	for _, dhcp := range []bool{false, true} {
		for _, failure := range []struct {
			name string
			err  error
		}{
			{"missing", fmt.Errorf("read lease: %w", os.ErrNotExist)},
			{"permission", fmt.Errorf("read lease: %w", os.ErrPermission)},
			{"malformed", errFixtureLeaseJSON},
		} {
			t.Run(fmt.Sprintf("dhcp=%t/%s", dhcp, failure.name), func(t *testing.T) {
				value := config.Default()
				value.WAN.DHCP = dhcp
				snapshot := Snapshot{Config: summarizeConfig(value)}
				snapshot.collectLease(service.LeaseState{}, failure.err)
				wantError := dhcp || failure.name != "missing"
				if got := snapshot.Errors["lease"] != ""; got != wantError {
					t.Fatalf("lease error present=%t, want %t: %+v", got, wantError, snapshot.Errors)
				}
				text := RenderSnapshot(snapshot)
				want := "DHCP lease: none recorded"
				if wantError {
					want = "DHCP lease: unavailable"
				}
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in report:\n%s", want, text)
				}
			})
		}
		value := config.Default()
		value.WAN.DHCP = dhcp
		snapshot := Snapshot{Config: summarizeConfig(value)}
		lease := service.LeaseState{Applied: true}
		snapshot.collectLease(lease, nil)
		if snapshot.Lease == nil || !snapshot.Lease.Applied || len(snapshot.Errors) != 0 {
			t.Fatalf("valid lease discarded for dhcp=%t: %+v", dhcp, snapshot)
		}
	}
}
