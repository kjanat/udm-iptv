package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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

func TestExportPreservesFullEvidence(t *testing.T) {
	t.Parallel()
	event := privateExportEvent()
	address, err := netlink.ParseAddr("192.168.1.1/24")
	if err != nil {
		t.Fatal(err)
	}
	event.Snapshot.Lease.Lease.ManagedAddresses = []netlink.Addr{*address}
	event.Snapshot.Network.IPv6Knobs = map[string]string{"customer-lan/accept_ra": "1"}
	original := marshalExportEvent(t, event)
	for _, format := range []string{"text", "jsonl"} {
		var output bytes.Buffer
		if err := ExportCapture(bytes.NewReader(original), &output, format); err != nil {
			t.Fatal(err)
		}
		if format == "jsonl" && !bytes.Equal(bytes.TrimSpace(output.Bytes()), original) {
			t.Fatalf("JSON export changed the event:\n%s", output.Bytes())
		}
		if format == "text" && !bytes.Contains(output.Bytes(), original) {
			t.Fatalf("text export omitted the complete event:\n%s", output.Bytes())
		}
	}
	if !bytes.Equal(original, marshalExportEvent(t, event)) {
		t.Fatal("export modified its source event")
	}
}

func TestExportPreservesUnknownFieldsAndLargeCounters(t *testing.T) {
	t.Parallel()
	const raw = `{"type":"future-event","source":"gateway","log":"gateway gateway ::ffff:10.0.0.25","extra":{"counter":18446744073709551615,"parameters":"keep <this> & that","options":{"vendorclass":"IPTV_RG"}}}`
	for _, format := range []string{"jsonl", "text"} {
		var output bytes.Buffer
		if err := ExportCapture(strings.NewReader(raw), &output, format); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), raw) {
			t.Fatalf("%s lost unknown fields, address relationships or counter precision: %s", format, output.String())
		}
	}
}

func TestExportPreservesMarkersAndLegacyLabels(t *testing.T) {
	t.Parallel()
	const input = `{"type":"marker","privacy":"sanitized","time":"2026-09-23T00:00:00Z","message":"channel changed; picture frozen","ipv4Order":["10.0.0.2","10.0.0.1"]}`
	var output bytes.Buffer
	if err := ExportCapture(strings.NewReader(input), &output, "jsonl"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != input {
		t.Fatal("export changed existing evidence or falsely relabelled old sanitized data")
	}
}

func TestExportRejectsInvalidRecordsWithoutDroppingEarlierEvidence(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`{"type":"initial"}`, "null", "[]", `{"type":`} {
		t.Run(input, func(t *testing.T) {
			const first = `{"type":"log","log":"keep this record"}`
			var output bytes.Buffer
			err := ExportCapture(strings.NewReader(first+"\n"+input), &output, "jsonl")
			if err == nil || strings.TrimSpace(output.String()) != first {
				t.Fatalf("partial capture = %q, error = %v", output.String(), err)
			}
		})
	}
	var output bytes.Buffer
	if err := ExportCapture(strings.NewReader(`{"type":"initial"}`), &output, "text"); !errors.Is(err, errExportSnapshotMissing) {
		t.Fatalf("missing snapshot: %v", err)
	}
	if err := ExportCapture(strings.NewReader("{}"), &output, "invalid"); !errors.Is(err, errExportFormat) {
		t.Fatalf("invalid format: %v", err)
	}
}

type failedExportWriter struct{}

func (failedExportWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestExportPropagatesOutputFailure(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"jsonl", "text"} {
		err := ExportCapture(strings.NewReader(`{"type":"log","log":"evidence"}`), failedExportWriter{}, format)
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("%s: %v", format, err)
		}
	}
}

func marshalExportEvent(t *testing.T, event Event) []byte {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
