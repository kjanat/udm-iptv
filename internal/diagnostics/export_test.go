package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/mroute"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/kjanat/udm-iptv/internal/service"
)

func privateExportEvent() Event {
	return Event{
		Time: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC), Type: EventInitial,
		Message: strings.Join([]string{"gateway", "gateway", "password=secret"}, " "), Log: "::ffff:10.0.0.25 token=secret",
		Snapshot: &Snapshot{
			Network:   networkStatus{Target: "eth8", Addresses: []string{"192.168.1.1/24", "192.168.1.2/24"}},
			Multicast: &MulticastInfo{Entries: []mroute.Route{{Source: netip.MustParseAddr("192.168.1.1"), Group: netip.MustParseAddr("239.2.3.4"), Packets: 42}}},
			Lease:     &service.LeaseState{Lease: network.Lease{Address: "192.168.1.1", Options: map[string]string{"secret-key": "secret-value"}}},
		},
	}
}

func TestSanitizedExportCoversManagedAddressMasksAndSysctlKeys(t *testing.T) {
	t.Parallel()
	private := privateExportEvent()
	address, err := netlink.ParseAddr("192.168.1.1/24")
	if err != nil {
		t.Fatal(err)
	}
	private.Snapshot.Lease.Lease.ManagedAddresses = []netlink.Addr{*address}
	private.Snapshot.Network.IPv6Knobs = map[string]string{"customer-lan/accept_ra": "1"}
	clean := sanitizeExportEvent(t, newTestExporter(t), private)
	encoded := marshalExportEvent(t, clean)
	if bytes.Contains(encoded, []byte("customer-lan")) {
		t.Fatal("sysctl key leaked interface identity")
	}
	if !strings.Contains(RenderEvent(clean), strings.Join(clean.AddressOrder, " < ")) {
		t.Fatal("text lost election order")
	}
	if !bytes.Equal(clean.Snapshot.Lease.Lease.ManagedAddresses[0].Mask, address.Mask) {
		t.Fatal("prefix mask changed")
	}
	if clean.Snapshot.Lease.Lease.ManagedAddresses[0].IP.String() != clean.Snapshot.Lease.Lease.Address {
		t.Fatal("managed address lost its identity across lease fields")
	}
}

func TestSanitizedExportRejectsMissingSnapshots(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	err := ExportCapture(strings.NewReader(`{"type":"initial"}`), &output, "text")
	if !errors.Is(err, errExportSnapshotMissing) {
		t.Fatalf("missing snapshot: %v", err)
	}
}

func TestSanitizedExportPreservesRelationshipsWithoutMutatingPrivateData(t *testing.T) {
	t.Parallel()
	exporter := newTestExporter(t)
	private := privateExportEvent()
	before := marshalExportEvent(t, private)
	clean := sanitizeExportEvent(t, exporter, private)
	assertExportOmitsPrivateValues(t, clean)
	assertExportAddressRelationships(t, clean)
	assertExportAliasScope(t, exporter, private, clean)
	if clean.Privacy != PrivacySanitized || clean.Time != private.Time {
		t.Fatal("export lost mode or timestamp")
	}
	if !bytes.Equal(before, marshalExportEvent(t, private)) {
		t.Fatal("sanitizing mutated private capture")
	}
}

func newTestExporter(t *testing.T) *Exporter {
	t.Helper()
	exporter, err := NewExporter()
	if err != nil {
		t.Fatal(err)
	}
	return exporter
}

func sanitizeExportEvent(t *testing.T, exporter *Exporter, private Event) Event {
	t.Helper()
	clean, err := exporter.Event(private)
	if err != nil {
		t.Fatal(err)
	}
	return clean
}

func marshalExportEvent(t *testing.T, event Event) []byte {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertExportOmitsPrivateValues(t *testing.T, clean Event) {
	t.Helper()
	encoded := marshalExportEvent(t, clean)
	for _, secret := range []string{"192.168.1.1", "192.168.1.2", "239.2.3.4", "10.0.0.25", "gateway", "secret", "eth8"} {
		if bytes.Contains(encoded, []byte(secret)) || strings.Contains(RenderEvent(clean), secret) {
			t.Fatalf("export contains private value %q", secret)
		}
	}
}

func assertExportAddressRelationships(t *testing.T, clean Event) {
	t.Helper()
	prefix := netip.MustParsePrefix(clean.Snapshot.Network.Addresses[0])
	if prefix.Bits() != 24 || prefix.Addr().String() != clean.Snapshot.Lease.Lease.Address || prefix.Addr() != clean.Snapshot.Multicast.Entries[0].Source {
		t.Fatal("one address lost its cross-field identity or prefix length")
	}
	if !clean.Snapshot.Multicast.Entries[0].Group.IsMulticast() || clean.Snapshot.Multicast.Entries[0].Packets != 42 {
		t.Fatal("multicast role or counter changed")
	}
	if len(clean.AddressOrder) != 2 || clean.AddressOrder[0] != prefix.Addr().String() {
		t.Fatal("querier election ordering lost")
	}
	if !strings.Contains(RenderEvent(clean), "IPv4 aliases in original numerical order: "+strings.Join(clean.AddressOrder, " < ")) {
		t.Fatal("text export lost original numerical ordering")
	}
}

func assertExportAliasScope(t *testing.T, exporter *Exporter, private, clean Event) {
	t.Helper()
	second := sanitizeExportEvent(t, exporter, private)
	if second.Snapshot.Lease.Lease.Address != clean.Snapshot.Lease.Lease.Address {
		t.Fatal("alias changed within capture")
	}
	separate := sanitizeExportEvent(t, newTestExporter(t), private)
	if separate.Snapshot.Lease.Lease.Address == clean.Snapshot.Lease.Lease.Address {
		t.Fatal("exports share aliases")
	}
}

func TestSanitizedExportRemovesRepeatedHostnamesAndMappedAddresses(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "jsonl"} {
		input := Event{Type: EventLog, Log: strings.Join([]string{"gateway", "gateway", "::ffff:10.0.0.25", "password=swordfish"}, " ")}
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := ExportCapture(bytes.NewReader(encoded), &output, format); err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"gateway", "10.0.0.25", "swordfish"} {
			if strings.Contains(output.String(), secret) {
				t.Fatalf("%s leaked %s", format, secret)
			}
		}
	}
}
