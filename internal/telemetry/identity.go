package telemetry

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/kjanat/udm-iptv/internal/config"
)

// NetworkIdentity is measured egress identity, not necessarily the IPTV provider
// (VPNs, multi-WAN and wholesale networks can produce a different operator).
type NetworkIdentity struct {
	IP         string `json:"public_ip,omitempty"`
	PTR        string `json:"ptr,omitempty"`
	Provider   string `json:"detected_provider"`
	Method     string `json:"detection_method"`
	Confidence string `json:"confidence"`
	Status     string `json:"lookup_status"`
}

const unknown = "unknown"

const (
	ipLookupClientTimeout = 2 * time.Second
	// ipLookupTimeout bounds the combined HTTPS and PTR lookup.
	ipLookupTimeout = 3 * time.Second
	// ipv4TextLimit bounds the IP address response; the longest IPv4 text is 15 bytes.
	ipv4TextLimit = 65
)

func (r *Reporter) networkEnabled() bool {
	if r == nil || r.client == nil || !r.settings.Enabled || !r.settings.NetworkIdentity {
		return false
	}
	if r.configPath == "" {
		return true
	}
	value, err := config.Load(r.configPath)

	return err == nil && value.Telemetry.Enabled && value.Telemetry.NetworkIdentity
}

// LookupNetwork performs no request until explicitly invoked by the application.
// Both HTTPS discovery and DNS share a bounded deadline; no credentials are used.
func LookupNetwork(parent context.Context) NetworkIdentity {
	client := &http.Client{
		Timeout: ipLookupClientTimeout, Transport: HTTPTransport(http.DefaultTransport),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	return lookupNetwork(parent, client, "https://api.ipify.org", net.DefaultResolver.LookupAddr)
}

func lookupNetwork(parent context.Context, client *http.Client, endpoint string, ptr func(context.Context, string) ([]string, error)) NetworkIdentity {
	ctx, cancel := context.WithTimeout(parent, ipLookupTimeout)
	defer cancel()
	result := NetworkIdentity{Status: "unavailable"}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return cleanIdentity(result)
	}
	response, err := client.Do(request)
	if err != nil {
		return cleanIdentity(result)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return cleanIdentity(result)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, ipv4TextLimit))
	if err != nil || len(data) > ipv4TextLimit-1 {
		return cleanIdentity(result)
	}
	address, err := netip.ParseAddr(strings.TrimSpace(string(data)))
	if err != nil || !publicAddress(address) {
		return cleanIdentity(result)
	}
	result.IP, result.Status = address.String(), "ip-only"
	if names, err := ptr(ctx, result.IP); err == nil && len(names) != 0 {
		result.PTR = names[0]
	}

	return cleanIdentity(result)
}

func publicAddress(address netip.Addr) bool {
	address = address.Unmap()

	return address.IsGlobalUnicast() && !address.IsPrivate() && !netip.MustParsePrefix("100.64.0.0/10").Contains(address)
}

var dnsName = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?\.?$`)

const dnsLabelLimit = 63

func validPointerName(value string) bool {
	if !dnsName.MatchString(value) {
		return false
	}
	for label := range strings.SplitSeq(strings.TrimSuffix(value, "."), ".") {
		if len(label) == 0 || len(label) > dnsLabelLimit || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}

	return true
}

func cleanIdentity(value NetworkIdentity) NetworkIdentity {
	result := NetworkIdentity{Provider: unknown, Method: "none", Confidence: unknown, Status: "unavailable"}
	address, err := netip.ParseAddr(value.IP)
	if err != nil || !publicAddress(address) {
		return result
	}
	result.IP, result.Status = address.Unmap().String(), "ip-only"
	if !validPointerName(value.PTR) {
		return result
	}
	result.PTR, result.Status = value.PTR, "ip-and-ptr"
	if provider, ok := config.DefaultCatalog().ProviderByPointerName(value.PTR); ok {
		result.Provider, result.Method, result.Confidence = provider.ID, "ptr-suffix", "low"
	}

	return result
}
