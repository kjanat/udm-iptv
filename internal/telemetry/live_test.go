//go:build sentrylive

package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// Explicitly opt in: UDM_IPTV_SENTRY_VERIFY=1 go test -tags sentrylive
// ./internal/telemetry -run '^TestLiveSentryDelivery$' -count=1 -v
// Supply the intended DSN using -ldflags '-X github.com/kjanat/udm-iptv/internal/telemetry.DSN=<dsn>'.
// This sends synthetic telemetry to the configured maintainer project.
func TestLiveSentryDelivery(t *testing.T) {
	if os.Getenv("UDM_IPTV_SENTRY_VERIFY") != "1" {
		t.Skip("live Sentry verification requires explicit opt-in")
	}
	transport := &verificationTransport{HTTPTransport: sentry.NewHTTPTransport()}
	transport.BufferSize = 32
	version := "verify-" + time.Now().UTC().Format("20060102T150405Z")
	reporter, err := newReporter(testSettings(), version, transport, DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()
	// No router inspection, device metadata or real operation is performed.
	err = reporter.Run(context.Background(), "install", func(ctx context.Context) error {
		return reporter.Run(ctx, "service.health", func(context.Context) error {
			return errors.New("synthetic verification failure")
		})
	})
	if err == nil {
		t.Fatal("synthetic operation unexpectedly succeeded")
	}
	reporter.Gauge(context.Background(), "daemon.uptime", 42)
	if !reporter.client.Flush(10 * time.Second) {
		t.Fatal("Sentry flush timed out")
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	seen := make(map[string]bool)
	for _, delivery := range transport.deliveries {
		t.Logf("Sentry %s: HTTP %d", delivery.kind, delivery.status)
		if delivery.status < 200 || delivery.status >= 300 {
			t.Errorf("Sentry rejected %s: HTTP %d", delivery.kind, delivery.status)
		}
		seen[delivery.kind] = true
	}
	for _, kind := range []string{"event", "transaction", "log", "trace_metric"} {
		if !seen[kind] {
			t.Errorf("no HTTP delivery observed for %s", kind)
		}
	}
	t.Logf("Verification release: udm-iptv@%s", version)
}

type verificationTransport struct {
	*sentry.HTTPTransport
	mu         sync.Mutex
	deliveries []struct {
		kind   string
		status int
	}
}

func (transport *verificationTransport) Configure(options sentry.ClientOptions) {
	options.HTTPClient = &http.Client{Timeout: 2 * time.Second, Transport: transport}
	transport.HTTPTransport.Configure(options)
}

func (transport *verificationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.GetBody == nil {
		return nil, errors.New("verification needs a replayable envelope")
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(body)
	var envelope map[string]any
	var item struct {
		Type string `json:"type"`
	}
	err = decoder.Decode(&envelope)
	if err == nil {
		err = decoder.Decode(&item)
	}
	_ = body.Close()
	if err != nil {
		return nil, fmt.Errorf("decode verification envelope: %w", err)
	}
	response, err := http.DefaultTransport.RoundTrip(request)
	status := 0
	if response != nil {
		status = response.StatusCode
	}
	transport.mu.Lock()
	transport.deliveries = append(transport.deliveries, struct {
		kind   string
		status int
	}{item.Type, status})
	transport.mu.Unlock()
	return response, err
}
