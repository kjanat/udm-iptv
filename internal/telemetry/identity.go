package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/network"
)

// NetworkIdentity is measured egress identity, not necessarily the IPTV provider
// (VPNs, multi-WAN and wholesale networks can produce a different operator).
type NetworkIdentity struct {
	IP          string    `json:"public_ip,omitempty"`
	IPSource    string    `json:"ip_source,omitempty"`
	ASN         string    `json:"asn,omitempty"`
	PTR         string    `json:"ptr,omitempty"`
	Provider    string    `json:"detected_provider"`
	Method      string    `json:"detection_method"`
	Confidence  string    `json:"confidence"`
	Status      string    `json:"lookup_status"`
	LookupError string    `json:"lookup_error,omitempty"`
	HTTPSMillis int64     `json:"https_ms,omitempty"`
	PTRMillis   int64     `json:"ptr_ms,omitempty"`
	ObservedAt  time.Time `json:"observed_at,omitzero"`
}

const unknown = "unknown"

// Where the public address came from.
const (
	ipSourceWAN   = "wan"
	ipSourceHTTPS = "https"
)

const (
	// httpsLookupTimeout bounds the public IP request on its own.
	httpsLookupTimeout = 3 * time.Second
	// ptrLookupTimeout bounds the reverse DNS lookup on its own.
	ptrLookupTimeout = 3 * time.Second
	// ipv4TextLimit bounds the IP address response; the longest IPv4 text is 15 bytes.
	ipv4TextLimit    = 65
	publicIPEndpoint = "https://api.ipify.org"
)

// lookupBudget is the time each phase of the lookup gets.
type lookupBudget struct {
	https, ptr time.Duration
}

func defaultLookupBudget() lookupBudget {
	return lookupBudget{https: httpsLookupTimeout, ptr: ptrLookupTimeout}
}

// lookupSources are the ways an identity lookup learns the address and its owner.
type lookupSources struct {
	egress   func() (netip.Addr, error)
	client   *http.Client
	endpoint string
	ptr      func(context.Context, string) ([]string, error)
	catalog  config.Catalog
	budget   lookupBudget
}

func (r *Reporter) networkEnabled() bool {
	if r == nil || r.client == nil || !r.settings.Enabled || !r.settings.NetworkIdentity {
		return false
	}
	if r.configPath == "" {
		return true
	}
	value, err := config.Load(r.configPath)
	if err != nil {
		r.deliveryIssue("network", "read reporting settings: "+err.Error())
		return false
	}
	return value.Telemetry.Enabled && value.Telemetry.NetworkIdentity
}

// LookupNetwork performs no request until explicitly invoked by the application.
// The WAN address is read from the kernel; ipify is asked only when that
// address is not public. The provider comes from the networks built into the
// binary, and reverse DNS through the system resolver is the fallback. Each
// remote phase has its own deadline; no credentials are used.
func LookupNetwork(parent context.Context) NetworkIdentity {
	client := &http.Client{
		Transport:     HTTPTransport(http.DefaultTransport),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	return lookupNetwork(parent, lookupSources{
		egress: network.EgressAddress, client: client, endpoint: publicIPEndpoint,
		ptr: net.DefaultResolver.LookupAddr, catalog: config.DefaultCatalog(), budget: defaultLookupBudget(),
	})
}

func lookupNetwork(parent context.Context, sources lookupSources) NetworkIdentity {
	result := NetworkIdentity{Status: "unavailable", ObservedAt: time.Now().UTC()}
	address, ok := localEgress(sources.egress)
	if ok {
		result.IPSource = ipSourceWAN
	} else {
		started := time.Now()
		var err error
		address, err = publicIP(parent, sources.client, sources.endpoint, sources.budget.https)
		result.HTTPSMillis = time.Since(started).Milliseconds()
		if err != nil {
			result.LookupError = fmt.Sprintf("%v after %d ms", err, result.HTTPSMillis)
			return cleanIdentity(sources.catalog, result)
		}
		result.IPSource = ipSourceHTTPS
	}
	result.IP, result.Status = address.String(), "ip-only"
	if _, asn, found := sources.catalog.ProviderByAddress(address); found {
		result.ASN = asn
		return cleanIdentity(sources.catalog, result)
	}
	ctx, cancel := context.WithTimeout(parent, sources.budget.ptr)
	defer cancel()
	started := time.Now()
	names, err := sources.ptr(ctx, result.IP)
	result.PTRMillis = time.Since(started).Milliseconds()
	if err != nil {
		result.LookupError = fmt.Sprintf("reverse DNS lookup: %v after %d ms", err, result.PTRMillis)
	} else if len(names) != 0 {
		result.PTR = names[0]
	}

	return cleanIdentity(sources.catalog, result)
}

func localEgress(egress func() (netip.Addr, error)) (netip.Addr, bool) {
	if egress == nil {
		return netip.Addr{}, false
	}
	address, err := egress()
	if err != nil || !publicAddress(address) {
		return netip.Addr{}, false
	}

	return address, true
}

func publicIP(parent context.Context, client *http.Client, endpoint string, timeout time.Duration) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("create IP lookup request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("request public IP: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("%w %s", errPublicIPStatus, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, ipv4TextLimit))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("read public IP response: %w", err)
	}
	if len(data) > ipv4TextLimit-1 {
		return netip.Addr{}, errPublicIPTooLong
	}
	address, err := netip.ParseAddr(strings.TrimSpace(string(data)))
	if err != nil || !publicAddress(address) {
		return netip.Addr{}, fmt.Errorf("%w: %q", errPublicIPInvalid, data)
	}

	return address, nil
}

var (
	errPublicIPStatus  = errors.New("public IP lookup returned")
	errPublicIPTooLong = errors.New("public IP response exceeds expected address length")
	errPublicIPInvalid = errors.New("invalid public IP response")
)

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

func cleanIdentity(catalog config.Catalog, value NetworkIdentity) NetworkIdentity {
	result := NetworkIdentity{
		Provider: unknown, Method: "none", Confidence: unknown, Status: "unavailable", LookupError: value.LookupError,
		IPSource: value.IPSource, HTTPSMillis: value.HTTPSMillis, PTRMillis: value.PTRMillis, ObservedAt: value.ObservedAt,
	}
	if value.Status == "rate-limited" {
		result.Status = value.Status
	}
	address, err := netip.ParseAddr(value.IP)
	if err != nil || !publicAddress(address) {
		result.IPSource = ""
		return result
	}
	result.IP, result.Status = address.Unmap().String(), "ip-only"
	if provider, ok := catalog.ProviderByASN(value.ASN); ok {
		result.ASN, result.Status = value.ASN, "ip-and-asn"
		result.Provider, result.Method, result.Confidence = provider.ID, "asn", "medium"
		return result
	}
	if !validPointerName(value.PTR) {
		if value.PTR != "" {
			result.LookupError = fmt.Sprintf("invalid reverse DNS hostname: %q", value.PTR)
		}
		return result
	}
	result.PTR, result.Status = value.PTR, "ip-and-ptr"
	if provider, ok := catalog.ProviderByPointerName(value.PTR); ok {
		result.Provider, result.Method, result.Confidence = provider.ID, "ptr-suffix", "low"
	}

	return result
}
