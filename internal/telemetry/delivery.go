package telemetry

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"

	"github.com/kjanat/udm-iptv/internal/config"
)

var errTelemetryDisabled = errors.New("telemetry disabled in saved configuration")

// deliveryIssue reports locally, never through the pipeline that just failed.
// Repeated drops retain a count; Close prints the aggregate without flooding the
// router's journal during an exhausted budget or a disconnected network.
func (r *Reporter) deliveryIssue(kind, reason string) {
	if r == nil {
		return
	}
	r.deliveryMu.Lock()
	defer r.deliveryMu.Unlock()
	if r.deliveryCounts == nil {
		r.deliveryCounts = make(map[string]uint64)
	}
	key := kind + ": " + reason
	r.deliveryCounts[key]++
	if r.deliveryCounts[key] == 1 {
		_, _ = fmt.Fprintf(r.deliveryWriter(), "udm-iptv telemetry: %s; delivery incomplete\n", key)
	}
}

func (r *Reporter) deliveryWriter() io.Writer {
	if r.deliveryOutput != nil {
		return r.deliveryOutput
	}
	return os.Stderr
}

func (r *Reporter) deliverySummary() {
	r.deliveryMu.Lock()
	defer r.deliveryMu.Unlock()
	keys := make([]string, 0, len(r.deliveryCounts))
	for key := range r.deliveryCounts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if count := r.deliveryCounts[key]; count > 1 {
			_, _ = fmt.Fprintf(r.deliveryWriter(), "udm-iptv telemetry: %d delivery issues: %s\n", count, key)
		}
	}
}

// Flush waits for SDK queues to drain. Success is not a server acknowledgement;
// transport failures are reported separately by deliveryTransport.
func (r *Reporter) Flush() bool {
	if r == nil || r.client == nil {
		return true
	}
	if !r.client.Flush(sentryRequestTimeout) {
		r.deliveryIssue("flush", "queued telemetry did not drain before the deadline")
		return false
	}
	return true
}

type deliveryTransport struct {
	reporter *Reporter
	base     http.RoundTripper
}

func (transport deliveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := transport.reporter.deliveryAllowed(); err != nil {
		return nil, err
	}
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		transport.reporter.deliveryIssue("transport", err.Error())
		return response, fmt.Errorf("send telemetry request: %w", err)
	}
	if response.StatusCode >= http.StatusBadRequest {
		transport.reporter.deliveryIssue("transport", fmt.Sprintf("server rejected telemetry: HTTP %d", response.StatusCode))
	}
	if limits := response.Header.Get("X-Sentry-Rate-Limits"); limits != "" {
		transport.reporter.deliveryIssue("transport", "server telemetry rate limits: "+limits)
	}
	return response, nil
}

func (r *Reporter) deliveryAllowed() error {
	if r.configPath == "" {
		return nil
	}
	value, err := config.Load(r.configPath)
	if err != nil {
		r.deliveryIssue("transport", "cannot read telemetry settings: "+err.Error())
		return fmt.Errorf("check telemetry delivery settings: %w", err)
	}
	if !reportingEnabled(value.Telemetry) {
		return errTelemetryDisabled
	}
	return nil
}
