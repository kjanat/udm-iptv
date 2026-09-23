package telemetry

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
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

func TestLineWriterPreservesLongAndTrailingEvidence(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	writer := r.LineWriter(t.Context(), "proxy")
	evidence := "  " + strings.Repeat("évidence", 1300) + "  \n\ntrailing text "
	// The first write ends before the newline and exceeds the old tail-only
	// buffer. This is the actual subprocess chunking path that lost prefixes.
	for _, part := range []string{evidence[:lineLimit+100], evidence[lineLimit+100:]} {
		if _, err := io.WriteString(writer, part); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	var actual strings.Builder
	for _, event := range transport.events {
		for _, entry := range event.Logs {
			if entry.Attributes["line.empty"].AsInterface() != true {
				actual.WriteString(entry.Body)
			}
			if entry.Attributes["line.end"].AsInterface() == true {
				actual.WriteByte('\n')
			}
		}
	}
	if actual.String() != evidence {
		t.Fatalf("stream evidence changed: got %d bytes, want %d", actual.Len(), len(evidence))
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

func TestDeliveryFailuresAreVisibleLocally(t *testing.T) {
	transport := &refusingFlushTransport{}
	r, err := newTestReporter(testSettings(), transport)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	r.deliveryOutput = &output
	r.counts["logs"] = logsPerMinute
	r.window = time.Now()
	for range 2 {
		if r.allow("logs", logsPerMinute) {
			t.Fatal("exhausted budget allowed more logs")
		}
	}
	r.Close()
	for _, expected := range []string{"budget exhausted", "2 delivery issues", "queued telemetry did not drain"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q in %q", expected, output.String())
		}
	}
	if r.client.Options().DisableClientReports {
		t.Fatal("SDK drop reports disabled")
	}
}

func TestBreadcrumbEvictionsAreCountedLocally(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	var output bytes.Buffer
	r.deliveryOutput = &output
	for range maxBreadcrumbs + 2 {
		r.breadcrumb("operation", sentry.LevelInfo)
	}
	r.failure(t.Context(), "install", errOperationFailed, false)
	r.Close()
	if len(failureEvents(transport.events)[0].Breadcrumbs) != maxBreadcrumbs || !strings.Contains(output.String(), "2 delivery issues: breadcrumbs") {
		t.Fatalf("eviction count missing or history limit changed: %s", output.String())
	}
}

type rejectedDelivery struct{}

func (rejectedDelivery) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusRequestEntityTooLarge, Header: make(http.Header), Body: http.NoBody}, nil
}

func TestTransportRejectionIsVisibleLocally(t *testing.T) {
	var output bytes.Buffer
	r := &Reporter{deliveryOutput: &output}
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
	if !strings.Contains(output.String(), "HTTP 413") {
		t.Fatalf("rejection invisible: %s", output.String())
	}
}

func TestTransportHonorsRevocationBeforeClientReportDelivery(t *testing.T) {
	var output bytes.Buffer
	r := &Reporter{deliveryOutput: &output, configPath: filepath.Join(t.TempDir(), "config.json")}
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
	if !errors.Is(err, errTelemetryDisabled) || response != nil || output.Len() != 0 {
		t.Fatalf("explicit opt-out sent traffic or warned: response=%v err=%v output=%s", response, err, output.String())
	}
}

func TestLineWriterFlushPreservesInvalidUTF8(t *testing.T) {
	r, transport := newRecordingReporter(t, testSettings())
	writer := r.LineWriter(t.Context(), "proxy")
	evidence := bytes.Repeat([]byte{0x80}, lineLimit+1)
	if _, err := writer.Write(evidence); err != nil {
		t.Fatal(err)
	}
	FlushLines(writer)
	r.Close()
	var actual []byte
	for _, event := range transport.events {
		for _, entry := range event.Logs {
			if entry.Attributes["line.encoding"].AsInterface() != "base64" {
				t.Fatal("binary evidence missing encoding marker")
			}
			part, err := base64.StdEncoding.DecodeString(entry.Body)
			if err != nil {
				t.Fatal(err)
			}
			actual = append(actual, part...)
		}
	}
	if !bytes.Equal(actual, evidence) {
		t.Fatal("binary line lost bytes or Flush and Close duplicated the tail")
	}
}
