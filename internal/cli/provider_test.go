package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

func TestProviderSuggestionUsesEvidence(t *testing.T) {
	for provider, want := range map[string]string{"kpn": "kpn", "xs4all": "xs4all", "freedom": "freedom", "tweak": "tweak", "unknown": "", "custom": ""} {
		identity := telemetry.NetworkIdentity{Provider: provider, Method: "ptr-suffix", Confidence: "low", Status: "ip-and-ptr"}
		if got := suggestedProvider(identity); got != want {
			t.Errorf("%s: %s", provider, got)
		}
		identity.Method = "none"
		if suggestedProvider(identity) != "" {
			t.Fatal("profile default treated as evidence")
		}
	}
}

func TestProviderLookupOptOutAndPrivateOutput(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		var out bytes.Buffer
		calls := 0
		app := &Application{Err: &out, networkIdentity: func(context.Context) telemetry.NetworkIdentity {
			calls++

			return telemetry.NetworkIdentity{IP: "203.0.113.10", PTR: "private-customer.kpn.net", Provider: "kpn", Method: "ptr-suffix", Confidence: "low", Status: "ip-and-ptr"}
		}}
		settings := config.Default().Telemetry
		settings.NetworkIdentity = enabled
		suggestion, err := app.suggestProvider(context.Background(), settings)
		if err != nil {
			t.Fatal(err)
		}
		if (calls == 1) != enabled {
			t.Fatal("lookup ignored opt-out")
		}
		if (suggestion == "kpn") != enabled {
			t.Fatalf("suggestion %q for networkIdentity=%t", suggestion, enabled)
		}
		if strings.Contains(out.String(), "203.0.113") || strings.Contains(out.String(), "private-customer") {
			t.Fatal("printed network identity")
		}
	}
}
