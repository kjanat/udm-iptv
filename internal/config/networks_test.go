package config

import (
	"bytes"
	"compress/gzip"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func gzipText(t *testing.T, text string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return buffer.Bytes()
}

// Every autonomous system the catalog lists has prefixes in the embedded
// table, and the table names no system outside the catalog, so a catalog
// change without a regenerated table fails here.
func TestProviderNetworksMatchTheCatalog(t *testing.T) {
	t.Parallel()
	networks, err := EmbeddedProviderNetworks()
	if err != nil {
		t.Fatal(err)
	}
	catalog := DefaultCatalog()
	seen := map[string]bool{}
	for _, network := range networks {
		if _, ok := catalog.ProviderByASN(network.ASN); !ok {
			t.Errorf("%s in the table belongs to no provider", network.ASN)
		}
		seen[network.ASN] = true
	}
	for _, provider := range catalog.Providers {
		for _, asn := range provider.ASNs {
			if !seen[asn] {
				t.Errorf("%s of %s has no prefixes in the table; run go run ./cmd/providernetworks", asn, provider.ID)
			}
		}
	}
}

func TestProviderByAddressPrefersTheLongestPrefix(t *testing.T) {
	t.Parallel()
	catalog := DefaultCatalog()
	networks := []ProviderNetwork{
		{ASN: "AS1136", Prefix: netip.MustParsePrefix("10.0.0.0/8")},
		{ASN: "AS206238", Prefix: netip.MustParsePrefix("10.20.0.0/16")},
		{ASN: "AS64496", Prefix: netip.MustParsePrefix("10.30.0.0/16")},
		{ASN: "AS3320", Prefix: netip.MustParsePrefix("2001:db8::/32")},
	}
	for address, want := range map[string]string{"10.1.2.3": "kpn", "10.20.5.6": "freedom", "::ffff:10.20.5.6": "freedom", "2001:db8::1": "magentatv"} {
		provider, _, ok := catalog.providerByAddress(networks, netip.MustParseAddr(address))
		if !ok || provider.ID != want {
			t.Errorf("%s -> %q, %v; want %s", address, provider.ID, ok, want)
		}
	}
	if provider, asn, ok := catalog.providerByAddress(networks, netip.MustParseAddr("10.30.1.1")); ok || asn != "AS64496" {
		t.Errorf("system outside the catalog: %q %s %v", provider.ID, asn, ok)
	}
	if _, _, ok := catalog.providerByAddress(networks, netip.MustParseAddr("192.0.2.1")); ok {
		t.Error("address outside every network matched")
	}
}

func TestParseProviderNetworksRejectsMalformedLines(t *testing.T) {
	t.Parallel()
	if _, err := ParseProviderNetworks([]byte("plain")); err == nil {
		t.Fatal("accepted uncompressed input")
	}
	if _, err := ParseProviderNetworks(gzipText(t, "AS1136\n")); !errors.Is(err, errProviderNetworkLine) {
		t.Fatalf("line without a prefix: %v", err)
	}
	if _, err := ParseProviderNetworks(gzipText(t, "AS1136 10.0.0.0\n")); err == nil || !strings.Contains(err.Error(), "AS1136") {
		t.Fatalf("bare address accepted or unattributed: %v", err)
	}
}

func TestCatalogRejectsASystemListedTwice(t *testing.T) {
	t.Parallel()
	document := strings.Replace(validProfileDocument,
		`"providers": {"acme": {"name": "ACME", "countries": ["XX"], "profiles": ["acme"]}}`,
		`"providers": {"acme": {"name": "ACME", "countries": ["XX"], "profiles": ["acme"], "asns": ["AS64496"]}, "beta": {"name": "Beta", "countries": ["XX"], "profiles": ["acme"], "asns": ["AS64496"]}}`, 1)
	if _, err := ParseCatalog([]byte(document)); !errors.Is(err, errCatalogReference) {
		t.Fatalf("shared system accepted: %v", err)
	}
}
