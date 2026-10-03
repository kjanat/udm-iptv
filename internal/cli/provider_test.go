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
		identity := telemetry.NetworkIdentity{Provider: provider, Method: "ptr-suffix", Confidence: "low", Status: "ip-and-ptr", PTR: "customer.example.net."}
		if got := suggestedProvider(identity); got != want {
			t.Errorf("%s: %s", provider, got)
		}
		identity.Method = "none"
		if suggestedProvider(identity) != "" {
			t.Fatal("profile default treated as evidence")
		}
		network := telemetry.NetworkIdentity{Provider: provider, Method: "asn", Confidence: "medium", Status: "ip-and-asn", ASN: "AS64496"}
		if got := suggestedProvider(network); got != want {
			t.Errorf("%s by network: %s", provider, got)
		}
	}
}

func TestProviderSuggestionNamesTheNetwork(t *testing.T) {
	var out bytes.Buffer
	app := &Application{Err: &out, networkIdentity: func(context.Context) telemetry.NetworkIdentity {
		return telemetry.NetworkIdentity{IP: "203.0.113.10", ASN: "AS1136", Provider: "kpn", Method: "asn", Confidence: "medium", Status: "ip-and-asn"}
	}}
	suggestion, err := app.suggestProvider(context.Background(), config.Default().Telemetry)
	if err != nil || suggestion != "kpn" {
		t.Fatalf("suggestion %q, %v", suggestion, err)
	}
	if !strings.Contains(out.String(), "Suggested: kpn (your internet address is in kpn's network, AS1136).") || strings.Contains(out.String(), "203.0.113") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestProviderLookupFailureIsShown(t *testing.T) {
	var out bytes.Buffer
	app := &Application{Err: &out, networkIdentity: func(context.Context) telemetry.NetworkIdentity {
		return telemetry.NetworkIdentity{Provider: "unknown", Method: "none", Status: "unavailable", LookupError: "request public IP: context deadline exceeded after 3000 ms"}
	}}
	suggestion, err := app.suggestProvider(context.Background(), config.Default().Telemetry)
	if err != nil || suggestion != "" {
		t.Fatalf("suggestion %q, %v", suggestion, err)
	}
	if !strings.Contains(out.String(), "Provider unknown (request public IP: context deadline exceeded after 3000 ms). Choose manually.") {
		t.Fatalf("lookup failure hidden: %s", out.String())
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
