package telemetry

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/kjanat/udm-iptv/internal/config"
)

var errTelemetryDisabled = errors.New("telemetry disabled in saved configuration")

// deliveryIssue counts failures without writing telemetry diagnostics to stderr
// or feeding them back into the pipeline that just failed.
func (r *Reporter) deliveryIssue(kind, reason string) {
	if r == nil {
		return
	}
	r.deliveryMu.Lock()
	defer r.deliveryMu.Unlock()
	if r.deliveryCounts == nil {
		r.deliveryCounts = make(map[string]uint64)
	}
	r.deliveryCounts[kind+": "+reason]++
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
