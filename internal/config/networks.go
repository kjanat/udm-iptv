package config

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
)

//go:embed provider_networks.txt.gz
var embeddedProviderNetworks []byte

var errProviderNetworkLine = errors.New("provider network line is not \"ASN prefix\"")

// ProviderNetwork is one prefix announced by an autonomous system a catalog
// provider lists.
type ProviderNetwork struct {
	ASN    string
	Prefix netip.Prefix
}

var providerNetworks = sync.OnceValues(func() ([]ProviderNetwork, error) {
	return ParseProviderNetworks(embeddedProviderNetworks)
})

// EmbeddedProviderNetworks returns the table built into this binary.
func EmbeddedProviderNetworks() ([]ProviderNetwork, error) {
	return providerNetworks()
}

// ParseProviderNetworks reads gzipped "ASN prefix" lines.
func ParseProviderNetworks(data []byte) ([]ProviderNetwork, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open provider networks: %w", err)
	}
	var networks []ProviderNetwork
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		asn, text, ok := strings.Cut(scanner.Text(), " ")
		if !ok {
			return nil, fmt.Errorf("%w: %q", errProviderNetworkLine, scanner.Text())
		}
		prefix, err := netip.ParsePrefix(text)
		if err != nil {
			return nil, fmt.Errorf("provider network of %s: %w", asn, err)
		}
		networks = append(networks, ProviderNetwork{ASN: asn, Prefix: prefix})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read provider networks: %w", err)
	}

	return networks, nil
}

// ProviderByAddress finds the provider whose autonomous system announces the
// longest prefix containing address, and returns that system.
func (catalog Catalog) ProviderByAddress(address netip.Addr) (Provider, string, bool) {
	networks, err := providerNetworks()
	if err != nil {
		return Provider{}, "", false
	}

	return catalog.providerByAddress(networks, address)
}

func (catalog Catalog) providerByAddress(networks []ProviderNetwork, address netip.Addr) (Provider, string, bool) {
	address = address.Unmap()
	var best ProviderNetwork
	found := false
	for _, network := range networks {
		if network.Prefix.Contains(address) && (!found || network.Prefix.Bits() > best.Prefix.Bits()) {
			best, found = network, true
		}
	}
	if !found {
		return Provider{}, "", false
	}
	provider, ok := catalog.ProviderByASN(best.ASN)

	return provider, best.ASN, ok
}
