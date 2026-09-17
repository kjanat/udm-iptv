package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
)

// addressUpdateBuffer bounds pending netlink address updates before the
// sanitizer's watch loop applies backpressure.
const addressUpdateBuffer = 16

var errAddressObservationStopped = errors.New("address observation stopped")

var (
	macPattern = regexp.MustCompile(`(?i)(?:\b[0-9a-f]{2}[:-]){5}[0-9a-f]{2}\b|\b[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}\b`)
	ipPattern  = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b|\b[0-9a-fA-F:]{2,}%?[0-9A-Za-z_.-]*\b`)
)

// Sanitize redacts MAC addresses and IP addresses from text.
func Sanitize(text string) string {
	return sanitize(text)
}

func sanitize(text string) string {
	return sanitizeWithAddresses(text, assignedAddresses())
}

func sanitizeWithAddresses(text string, addresses []string) string {
	text = macPattern.ReplaceAllString(text, "<mac>")
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		pattern := regexp.MustCompile(`(?i)(^|[^0-9A-Za-z_-])(` + regexp.QuoteMeta(hostname) + `)([^0-9A-Za-z_-]|$)`)
		text = pattern.ReplaceAllString(text, "${1}<router-hostname>${3}")
	}
	for _, address := range addresses {
		pattern := regexp.MustCompile(`(^|[^0-9A-Fa-f:.])(` + regexp.QuoteMeta(address) + `)([^0-9A-Fa-f:.]|$)`)
		text = pattern.ReplaceAllString(text, "${1}<device-address>${3}")
	}

	return ipPattern.ReplaceAllStringFunc(text, redactAddressLiteral)
}

func redactAddressLiteral(candidate string) string {
	address := strings.Trim(candidate, "[](),")
	if zone := strings.LastIndexByte(address, '%'); zone >= 0 {
		address = address[:zone]
	}
	parsed, err := netip.ParseAddr(address)
	if err != nil {
		return candidate
	}
	if parsed.Is6() || localAddress(parsed) {
		return "<redacted-address>"
	}

	return candidate
}

// localAddress reports whether an address describes this network rather than
// a routable peer, and so must not reach a diagnostic report.
func localAddress(address netip.Addr) bool {
	return address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() ||
		address.IsMulticast() || address.IsUnspecified() || sharedAddress(address)
}

type diagnosticSanitizer struct {
	configPath string
	addresses  map[string]bool
	mutex      sync.RWMutex
}

func newDiagnosticSanitizer(configPath string) *diagnosticSanitizer {
	value := &diagnosticSanitizer{configPath: configPath, addresses: make(map[string]bool)}
	value.refresh()

	return value
}

func (value *diagnosticSanitizer) refresh() {
	value.observe(assignedAddresses())
	configured, err := config.Load(value.configPath)
	if err != nil || configured.WAN.StaticAddress == "" {
		return
	}
	if prefix, parseErr := netip.ParsePrefix(configured.WAN.StaticAddress); parseErr == nil {
		value.observe([]string{prefix.Addr().String()})
	}
}

func (value *diagnosticSanitizer) observe(addresses []string) {
	value.mutex.Lock()
	defer value.mutex.Unlock()
	for _, address := range addresses {
		if net.ParseIP(address) != nil {
			value.addresses[address] = true
		}
	}
}

func (value *diagnosticSanitizer) sanitize(text string) string {
	value.mutex.RLock()
	defer value.mutex.RUnlock()
	addresses := make([]string, 0, len(value.addresses))
	for address := range value.addresses {
		addresses = append(addresses, address)
	}

	return sanitizeWithAddresses(text, addresses)
}

func (value *diagnosticSanitizer) watch(ctx context.Context) (<-chan error, error) {
	failures := make(chan error, 1)
	updates := make(chan netlink.AddrUpdate, addressUpdateBuffer)
	options := netlink.AddrSubscribeOptions{
		ListExisting:  true,
		ErrorCallback: func(err error) { reportFailure(failures, err) },
	}
	err := netlink.AddrSubscribeWithOptions(updates, ctx.Done(), options)
	if err != nil {
		return nil, fmt.Errorf("subscribe to address updates: %w", err)
	}
	go value.observeUpdates(ctx, updates, failures)

	return failures, nil
}

// reportFailure keeps the first failure without blocking the netlink reader.
func reportFailure(failures chan<- error, cause error) {
	select {
	case failures <- cause:
	default:
	}
}

func (value *diagnosticSanitizer) observeUpdates(ctx context.Context, updates <-chan netlink.AddrUpdate, failures chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-updates:
			if !ok {
				if ctx.Err() == nil {
					reportFailure(failures, errAddressObservationStopped)
				}

				return
			}
			if update.LinkAddress.IP != nil {
				value.observe([]string{update.LinkAddress.IP.String()})
			}
		}
	}
}

func assignedAddresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var result []string
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			raw, _, found := strings.Cut(address.String(), "/")
			if found {
				result = append(result, raw)
			}
		}
	}

	return result
}

func sharedAddress(address netip.Addr) bool {
	prefix := netip.MustParsePrefix("100.64.0.0/10")

	return address.Is4() && prefix.Contains(address)
}

func sanitizePrefixes(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err == nil && localAddress(prefix.Addr()) {
			result = append(result, "<redacted-prefix>")
		} else {
			result = append(result, value)
		}
	}

	return result
}

func fallbackText(value string) string {
	if value == "" {
		return "unknown"
	}

	return value
}
