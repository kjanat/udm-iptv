package telemetry

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
)

func TestRoutineSuccessDoesNotConsumeTelemetry(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	for _, operation := range []string{"dhcp.renew", "dhcp.leasefail", "dhcp.nak", "service.health"} {
		if err := r.Run(t.Context(), operation, func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	if got := countProducts(transport.events); got != (productCounts{}) {
		t.Fatalf("routine successes consumed telemetry: %+v", got)
	}
}

func TestFailedRenewalRetainsErrorTraceAndOutput(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	_ = r.Run(t.Context(), "dhcp.renew", func(ctx context.Context) error {
		_, _ = io.WriteString(r.LineWriter(ctx, "udhcpc"), "renewal evidence\n")
		return errOperationFailed
	})
	r.Close()
	got := countProducts(transport.events)
	if got.failures != 1 || got.traces != 1 || got.logs != 1 {
		t.Fatalf("failed renewal lost telemetry: %+v", got)
	}
	failure := failureEvents(transport.events)[0]
	if len(failure.Attachments) != 1 || string(failure.Attachments[0].Payload) != "renewal evidence\n" {
		t.Fatal("failed renewal lost subprocess evidence")
	}
}

func TestWarningCoalescingSurvivesProcesses(t *testing.T) {
	directory := t.TempDir()
	now := time.Now()
	for index := range 10 {
		r := &Reporter{stateDir: directory}
		got := r.warningOccurrences("DHCP retry", now)
		if (index == 0 && got != 1) || (index > 0 && got != 0) {
			t.Fatalf("warning %d count = %d", index, got)
		}
	}
	r := &Reporter{stateDir: directory}
	if got := r.warningOccurrences("DHCP retry", now.Add(warningInterval)); got != 10 {
		t.Fatalf("coalesced count = %d, want 10", got)
	}
	if got := r.warningOccurrences("different failure", now); got != 1 {
		t.Fatal("a distinct warning was suppressed")
	}
}

func TestSubprocessTailIsBoundedAndIndependentOfLogBudget(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	r.counts["logs"], r.window = logsPerMinute, time.Now()
	writer := r.LineWriter(t.Context(), "proxy")
	prefix := bytes.Repeat([]byte("old\n"), outputTailLimit)
	_, _ = writer.Write(prefix)
	_, _ = io.WriteString(writer, "last failure\n")
	want := append(bytes.Clone(prefix), []byte("last failure\n")...)
	got := failureOutput(t, r, transport)
	if !bytes.Equal(got, want[len(want)-outputTailLimit:]) {
		t.Fatal("tail limit discarded the newest failure evidence")
	}
	if countProducts(transport.events).logs != 0 {
		t.Fatal("subprocess output consumed the exhausted log budget")
	}
}

func TestBufferedOutputHonorsLogRevocation(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	_, _ = io.WriteString(r.LineWriter(t.Context(), "proxy"), "before opt-out\n")
	r.configPath = filepath.Join(t.TempDir(), "config.json")
	value := configtest.Custom()
	value.Telemetry = testSettings()
	value.Telemetry.Logs = false
	if err := config.Save(r.configPath, value); err != nil {
		t.Fatal(err)
	}
	r.failure(t.Context(), "daemon", errOperationFailed, false)
	r.Close()
	failures := failureEvents(transport.events)
	if len(failures) != 1 || len(failures[0].Attachments) != 0 {
		t.Fatal("log opt-out did not remove buffered subprocess attachments")
	}
}
