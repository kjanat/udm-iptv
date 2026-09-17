package diagnostics

import (
	"net/netip"
	"os"
	"regexp"
	"strings"
)

var (
	macPattern = regexp.MustCompile(`(?i)(?:\b[0-9a-f]{2}[:-]){5}[0-9a-f]{2}\b|\b[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}\b`)
	ipPattern  = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b|\b[0-9a-fA-F:]{2,}%?[0-9A-Za-z_.-]*\b`)
)

// Sanitize redacts MAC addresses from diagnostic text.
func Sanitize(text string) string {
	return sanitize(text)
}

func sanitize(text string) string {
	text = macPattern.ReplaceAllString(text, "<mac>")
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		pattern := regexp.MustCompile(`(?i)(^|[^0-9A-Za-z_-])(` + regexp.QuoteMeta(hostname) + `)([^0-9A-Za-z_-]|$)`)
		text = pattern.ReplaceAllString(text, "${1}<router-hostname>${3}")
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
	if parsed.IsLoopback() || parsed.IsLinkLocalUnicast() || parsed.IsMulticast() || parsed.IsUnspecified() {
		return "<redacted-address>"
	}

	return candidate
}

func sanitizePrefixes(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err == nil && (prefix.Addr().IsLoopback() || prefix.Addr().IsLinkLocalUnicast() || prefix.Addr().IsMulticast() || prefix.Addr().IsUnspecified()) {
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
