package telemetry

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
)

func TestAttachmentPreservesCompleteEvidence(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	evidence := []byte(strings.Repeat("full diagnostic evidence\n", 20000) + "FINAL EVIDENCE")
	_ = r.Run(t.Context(), "install", func(ctx context.Context) error {
		Attach(ctx, evidence)
		return errOperationFailed
	})
	r.Close()
	attachment := failureEvents(transport.events)[0].Attachments[0]
	reader, err := gzip.NewReader(bytes.NewReader(attachment.Payload))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	decoded, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(decoded, evidence) {
		t.Fatalf("attachment evidence changed: %v", err)
	}
	if attachment.ContentType != "application/gzip" || !strings.HasSuffix(attachment.Filename, ".txt.gz") {
		t.Fatalf("compressed attachment mislabeled: %+v", attachment)
	}
}

func failureOutput(t *testing.T, r *Reporter, transport *recordingTransport) []byte {
	t.Helper()
	r.failure(t.Context(), "daemon", errOperationFailed, false)
	r.Close()
	for _, attachment := range failureEvents(transport.events)[0].Attachments {
		if attachment.Filename == "udm-iptv-proxy-tail.log" {
			return attachment.Payload
		}
	}
	t.Fatal("missing subprocess failure evidence")
	return nil
}

func TestLineWriterPreservesLongAndTrailingEvidence(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	writer := r.LineWriter(t.Context(), "proxy")
	evidence := "  " + strings.Repeat("évidence", 1300) + "  \n\ntrailing text "
	for _, part := range []string{evidence[:4196], evidence[4196:]} {
		if _, err := io.WriteString(writer, part); err != nil {
			t.Fatal(err)
		}
	}
	if actual := failureOutput(t, r, transport); string(actual) != evidence {
		t.Fatal("failure attachment changed subprocess evidence")
	}
}

func TestUnknownDeviceMetadataIsRetained(t *testing.T) {
	r, _ := newRecordingReporter(t, testSettings())
	defer r.Close()
	r.SetMetadata("UCGFUTURE", "8.2.1-rc.4", "UCGFUTURE.aarch64.custom", "ea99", "improxy", "custom-provider")
	if r.metadata["model"] != "UCGFUTURE" || r.metadata["firmware"] != "8.2.1-rc.4" || r.metadata["profile"] != "custom-provider" {
		t.Fatalf("new hardware metadata disappeared: %v", r.metadata)
	}
}

func TestPackageMetadataRetainsEpochAndLongVersion(t *testing.T) {
	r, _ := newRecordingReporter(t, testSettings())
	defer r.Close()
	version := "1:5.0.0+" + strings.Repeat("localbuild", 10)
	r.SetInstallation(version)
	if r.metadata["package_version"] != version {
		t.Fatal("valid package version was dropped")
	}
}

type refusingFlushTransport struct{ recordingTransport }

func (*refusingFlushTransport) FlushWithContext(context.Context) bool { return false }
func (*refusingFlushTransport) Flush(_ time.Duration) bool            { return false }

var _ sentry.Transport = (*refusingFlushTransport)(nil)

func captureTelemetryStderr(t *testing.T) func() {
	t.Helper()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = stderr
	t.Cleanup(func() {
		os.Stderr = previous
		_ = stderr.Close()
	})
	return func() {
		t.Helper()
		info, err := stderr.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != 0 {
			t.Fatal("telemetry delivery failures wrote to stderr")
		}
	}
}

func exhaustDailyLogBudget(t *testing.T, r *Reporter) {
	t.Helper()
	r.stateDir = t.TempDir()
	budget, err := openRateBudget(r.stateDir, "logs")
	if err != nil {
		t.Fatal(err)
	}
	record, _ := (rateRecord{}).rollOver(time.Now())
	record.usedDay = logsPerMinute * dailyBudgetFactor
	err = budget.write(record)
	budget.release()
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryFailuresStayOffStderr(t *testing.T) {
	assertQuiet := captureTelemetryStderr(t)
	transport := &refusingFlushTransport{}
	r, err := newTestReporter(testSettings(), transport)
	if err != nil {
		t.Fatal(err)
	}
	r.counts["logs"] = logsPerMinute
	r.window = time.Now()
	for range 2 {
		if r.allow("logs", logsPerMinute) {
			t.Fatal("exhausted budget allowed more logs")
		}
	}
	exhaustDailyLogBudget(t, r)
	clear(r.counts)
	for range 2 {
		if r.allow("logs", logsPerMinute) {
			t.Fatal("exhausted daily budget allowed more logs")
		}
	}
	r.Close()
	if r.deliveryCounts["logs: telemetry daily budget exhausted"] != 2 {
		t.Fatalf("daily drops not counted: %v", r.deliveryCounts)
	}
	if r.deliveryCounts["logs: per-minute telemetry budget exhausted"] != 2 || r.deliveryCounts["flush: queued telemetry did not drain before the deadline"] != 1 {
		t.Fatalf("delivery failures not counted: %v", r.deliveryCounts)
	}
	assertQuiet()
	if r.client.Options().DisableClientReports {
		t.Fatal("SDK drop reports disabled")
	}
}

func TestBreadcrumbEvictionsAreCountedLocally(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	for range maxBreadcrumbs + 2 {
		r.breadcrumb("operation", sentry.LevelInfo)
	}
	r.failure(t.Context(), "install", errOperationFailed, false)
	r.Close()
	if len(failureEvents(transport.events)[0].Breadcrumbs) != maxBreadcrumbs || r.deliveryCounts["breadcrumbs: oldest breadcrumb evicted by SDK history limit"] != 2 {
		t.Fatalf("eviction count missing or history limit changed: %v", r.deliveryCounts)
	}
}

type rejectedDelivery struct{}

func (rejectedDelivery) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusRequestEntityTooLarge, Header: make(http.Header), Body: http.NoBody}, nil
}

func TestTransportRejectionIsCountedLocally(t *testing.T) {
	r := &Reporter{}
	transport := deliveryTransport{reporter: r, base: rejectedDelivery{}}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.invalid/envelope", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if r.deliveryCounts["transport: server rejected telemetry: HTTP 413"] != 1 {
		t.Fatalf("rejection not counted: %v", r.deliveryCounts)
	}
}

func TestTransportHonorsRevocationBeforeClientReportDelivery(t *testing.T) {
	r := &Reporter{configPath: filepath.Join(t.TempDir(), "config.json")}
	value := configtest.Custom()
	value.Telemetry.Enabled = false
	if err := config.Save(r.configPath, value); err != nil {
		t.Fatal(err)
	}
	transport := deliveryTransport{reporter: r, base: rejectedDelivery{}}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.invalid/envelope", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if response != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if !errors.Is(err, errTelemetryDisabled) || response != nil || len(r.deliveryCounts) != 0 {
		t.Fatalf("explicit opt-out sent traffic or counted a failure: response=%v err=%v counts=%v", response, err, r.deliveryCounts)
	}
}

func TestLineWriterPreservesInvalidUTF8(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	writer := r.LineWriter(t.Context(), "proxy")
	evidence := bytes.Repeat([]byte{0x80}, 4097)
	if _, err := writer.Write(evidence); err != nil {
		t.Fatal(err)
	}
	if actual := failureOutput(t, r, transport); !bytes.Equal(actual, evidence) {
		t.Fatal("binary subprocess evidence lost bytes")
	}
}
