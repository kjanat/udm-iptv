package telemetry

import (
	"context"
	"encoding/json"
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
	ASNSource   string    `json:"asn_source,omitempty"`
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

// Where the ASN came from.
const (
	asnSourceTable = "table"
	asnSourceEdge  = "edge"
)

const (
	// httpsLookupTimeout bounds the edge request on its own.
	httpsLookupTimeout = 3 * time.Second
	// ptrLookupTimeout bounds the reverse DNS lookup on its own.
	ptrLookupTimeout = 3 * time.Second
	// edgeBodyLimit bounds the edge response; its JSON body stays well under 1 KiB.
	edgeBodyLimit    = 4096
	publicIPEndpoint = "https://udm-iptv.kjanat.dev/"
)

var asnPattern = regexp.MustCompile(`^AS[1-9][0-9]{0,9}$`)

// edgeIdentity is the JSON body the udm-iptv edge returns to a non-browser client.
type edgeIdentity struct {
	IP  string `json:"ip"`
	ASN string `json:"asn"`
}

// lookupBudget is the time each phase of the lookup gets.
type lookupBudget struct {
	https, ptr time.Duration
}

func defaultLookupBudget() lookupBudget {
	return lookupBudget{https: httpsLookupTimeout, ptr: ptrLookupTimeout}
}

// lookupSources are the ways an identity lookup learns the address and its owner.
type lookupSources struct {
	egress    func() (netip.Addr, error)
	client    *http.Client
	endpoint  string
	userAgent string
	ptr       func(context.Context, string) ([]string, error)
	catalog   config.Catalog
	budget    lookupBudget
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

// NetworkLookup returns the lookup the application runs when asked. The WAN
// address is read from the kernel and matched against the networks built into
// the binary. When that does not name the provider, the udm-iptv edge is asked
// for the address and the autonomous system announcing it, identified by a
// udm-iptv/<version> user agent. Reverse DNS through the system resolver is the
// last resort. Each remote phase has its own deadline; no credentials are used.
func NetworkLookup(version string) func(context.Context) NetworkIdentity {
	client := &http.Client{
		Transport:     HTTPTransport(http.DefaultTransport),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	sources := lookupSources{
		egress: network.EgressAddress, client: client, endpoint: publicIPEndpoint, userAgent: "udm-iptv/" + version,
		ptr: net.DefaultResolver.LookupAddr, catalog: config.DefaultCatalog(), budget: defaultLookupBudget(),
	}

	return func(parent context.Context) NetworkIdentity { return lookupNetwork(parent, sources) }
}

func lookupNetwork(parent context.Context, sources lookupSources) NetworkIdentity {
	result := NetworkIdentity{Status: "unavailable", ObservedAt: time.Now().UTC()}
	if address, ok := localEgress(sources.egress); ok {
		result.IP, result.IPSource = address.String(), ipSourceWAN
		if _, asn, found := sources.catalog.ProviderByAddress(address); found {
			result.ASN, result.ASNSource = asn, asnSourceTable
			return cleanIdentity(sources.catalog, result)
		}
	}
	started := time.Now()
	edge, err := edgeLookup(parent, sources)
	result.HTTPSMillis = time.Since(started).Milliseconds()
	if err != nil {
		result.LookupError = fmt.Sprintf("%v after %d ms", err, result.HTTPSMillis)
		if result.IP == "" {
			return cleanIdentity(sources.catalog, result)
		}
		return cleanIdentity(sources.catalog, pointerLookup(parent, sources, result))
	}
	result = mergeEdge(sources.catalog, result, edge)
	if _, known := sources.catalog.ProviderByASN(result.ASN); known {
		return cleanIdentity(sources.catalog, result)
	}

	return cleanIdentity(sources.catalog, pointerLookup(parent, sources, result))
}

// mergeEdge takes the edge's address when the kernel offered none or a
// different one, and its ASN, or the built-in table's for that address.
func mergeEdge(catalog config.Catalog, result NetworkIdentity, edge edgeAnswer) NetworkIdentity {
	if result.IP != edge.ip.String() {
		result.IP, result.IPSource = edge.ip.String(), ipSourceHTTPS
	}
	if edge.ASN != "" {
		result.ASN, result.ASNSource = edge.ASN, asnSourceEdge
	} else if _, asn, found := catalog.ProviderByAddress(edge.ip); found {
		result.ASN, result.ASNSource = asn, asnSourceTable
	}

	return result
}

func pointerLookup(parent context.Context, sources lookupSources, result NetworkIdentity) NetworkIdentity {
	ctx, cancel := context.WithTimeout(parent, sources.budget.ptr)
	defer cancel()
	started := time.Now()
	names, err := sources.ptr(ctx, result.IP)
	result.PTRMillis = time.Since(started).Milliseconds()
	if err != nil {
		result.LookupError = joinErrors(result.LookupError, fmt.Sprintf("reverse DNS lookup: %v after %d ms", err, result.PTRMillis))
	} else if len(names) != 0 {
		result.PTR = names[0]
	}

	return result
}

func joinErrors(first, second string) string {
	if first == "" {
		return second
	}

	return first + "; " + second
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

// edgeAnswer is the edge's view of this connection, with its address parsed.
type edgeAnswer struct {
	ip  netip.Addr
	ASN string
}

func edgeLookup(parent context.Context, sources lookupSources) (edgeAnswer, error) {
	ctx, cancel := context.WithTimeout(parent, sources.budget.https)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sources.endpoint, nil)
	if err != nil {
		return edgeAnswer{}, fmt.Errorf("create edge lookup request: %w", err)
	}
	request.Header.Set("User-Agent", sources.userAgent)
	request.Header.Set("Accept", "application/json")
	response, err := sources.client.Do(request)
	if err != nil {
		return edgeAnswer{}, fmt.Errorf("request edge identity: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return edgeAnswer{}, fmt.Errorf("%w %s", errEdgeStatus, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, edgeBodyLimit+1))
	if err != nil {
		return edgeAnswer{}, fmt.Errorf("read edge identity response: %w", err)
	}
	if len(data) > edgeBodyLimit {
		return edgeAnswer{}, errEdgeTooLong
	}

	return parseEdge(data)
}

func parseEdge(data []byte) (edgeAnswer, error) {
	var body edgeIdentity
	if err := json.Unmarshal(data, &body); err != nil {
		return edgeAnswer{}, fmt.Errorf("%w: %w", errEdgeInvalid, err)
	}
	address, err := netip.ParseAddr(body.IP)
	if err != nil || !publicAddress(address) {
		return edgeAnswer{}, fmt.Errorf("%w: address %q", errEdgeInvalid, body.IP)
	}
	if body.ASN != "" && !asnPattern.MatchString(body.ASN) {
		return edgeAnswer{}, fmt.Errorf("%w: ASN %q", errEdgeInvalid, body.ASN)
	}

	return edgeAnswer{ip: address.Unmap(), ASN: body.ASN}, nil
}

var (
	errEdgeStatus  = errors.New("edge identity lookup returned")
	errEdgeTooLong = errors.New("edge identity response exceeds the expected size")
	errEdgeInvalid = errors.New("invalid edge identity response")
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
	switch {
	case asnPattern.MatchString(value.ASN):
		result.ASN, result.ASNSource, result.Status = value.ASN, value.ASNSource, "ip-and-asn"
		if provider, ok := catalog.ProviderByASN(value.ASN); ok {
			result.Provider, result.Method, result.Confidence = provider.ID, "asn", "medium"
			return result
		}
	case value.ASN != "":
		result.LookupError = joinErrors(result.LookupError, fmt.Sprintf("invalid ASN: %q", value.ASN))
	}

	return cleanPointer(catalog, result, value.PTR)
}

func cleanPointer(catalog config.Catalog, result NetworkIdentity, pointer string) NetworkIdentity {
	if !validPointerName(pointer) {
		if pointer != "" {
			result.LookupError = joinErrors(result.LookupError, fmt.Sprintf("invalid reverse DNS hostname: %q", pointer))
		}
		return result
	}
	result.PTR = pointer
	if result.ASN == "" {
		result.Status = "ip-and-ptr"
	}
	if provider, ok := catalog.ProviderByPointerName(pointer); ok {
		result.Provider, result.Method, result.Confidence = provider.ID, "ptr-suffix", "low"
	}

	return result
}
