package diagnostics

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Privacy modes distinguish full local evidence from a sanitized export.
const (
	PrivacyPrivate   = "private"
	PrivacySanitized = "sanitized"
)

const privateAliasOctet = 10

var errExportSnapshotMissing = errors.New("capture snapshot event has no snapshot")

// Exporter applies privacy only at the output boundary. One exporter must be
// reused for an entire capture, so aliases survive address changes and repetition.
// A fresh random key prevents correlating aliases across independently made exports.
type Exporter struct {
	key       [32]byte
	addresses map[netip.Addr]netip.Addr
	used      map[netip.Addr]bool
}

// NewExporter creates an independent alias scope for one export.
func NewExporter() (*Exporter, error) {
	result := &Exporter{addresses: make(map[netip.Addr]netip.Addr), used: make(map[netip.Addr]bool)}
	if _, err := rand.Read(result.key[:]); err != nil {
		return nil, fmt.Errorf("create export identity: %w", err)
	}
	return result, nil
}

// Event returns a detached sanitized copy. Free text and DHCP option payloads
// are omitted: regex redaction cannot establish that arbitrary logs contain no secrets.
func (exporter *Exporter) Event(event Event) (Event, error) {
	if snapshotEvent(event.Type) && event.Snapshot == nil {
		return Event{}, errExportSnapshotMissing
	}
	event.AddressOrder = nil
	data, err := json.Marshal(event)
	if err != nil {
		return Event{}, fmt.Errorf("encode private event: %w", err)
	}
	clean, err := exporter.cleanJSON(data, "")
	if err != nil {
		return Event{}, err
	}
	var result Event
	if err := json.Unmarshal(clean, &result); err != nil {
		return Event{}, fmt.Errorf("decode sanitized event: %w", err)
	}
	result.Privacy = PrivacySanitized
	result.AddressOrder = exporter.addressOrder()
	return result, nil
}

func snapshotEvent(kind string) bool {
	return slices.Contains([]string{EventInitial, EventSample, EventFinal, "snapshot"}, kind)
}

func (exporter *Exporter) addressOrder() []string {
	// Preserve election evidence without exposing the original numeric addresses.
	var original []netip.Addr
	for address := range exporter.addresses {
		if address.Is4() && !address.IsMulticast() {
			original = append(original, address)
		}
	}
	slices.SortFunc(original, netip.Addr.Compare)
	result := make([]string, 0, len(original))
	for _, address := range original {
		result = append(result, exporter.addresses[address].String())
	}
	return result
}

func (exporter *Exporter) cleanJSON(data json.RawMessage, field string) (json.RawMessage, error) {
	switch data[0] {
	case '{':
		return exporter.cleanObject(data, field)
	case '[':
		return exporter.cleanArray(data, field)
	case '"':
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, fmt.Errorf("decode export text: %w", err)
		}
		return marshalExportValue(exporter.text(value, field))
	default:
		return data, nil
	}
}

func (exporter *Exporter) cleanObject(data json.RawMessage, field string) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("decode export object: %w", err)
	}
	delete(object, "options")
	cleanObject := make(map[string]json.RawMessage, len(object))
	for key, value := range object {
		clean, err := exporter.cleanJSON(value, key)
		if err != nil {
			return nil, err
		}
		if field == "ipv6Knobs" {
			key = "alias-" + exporter.alias(key)
		}
		cleanObject[key] = clean
	}
	return marshalExportValue(cleanObject)
}

func (exporter *Exporter) cleanArray(data json.RawMessage, field string) (json.RawMessage, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("decode export array: %w", err)
	}
	for i, value := range items {
		clean, err := exporter.cleanJSON(value, field)
		if err != nil {
			return nil, err
		}
		items[i] = clean
	}
	return marshalExportValue(items)
}

func marshalExportValue[T string | map[string]json.RawMessage | []json.RawMessage](value T) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode sanitized field: %w", err)
	}
	return data, nil
}

func (exporter *Exporter) text(value, field string) string {
	if value == "" || safeExportToken(field, value) || exportTimestamp(field, value) || exportMask(field, value) {
		return value
	}
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return exporter.address(prefix.Addr()).String() + "/" + strconv.Itoa(prefix.Bits())
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return exporter.address(address).String()
	}
	switch field {
	case "message", "log", "resumeError", "switches", "nativeProxy", "playback":
		return "[free text omitted from sanitized export]"
	}
	return "alias-" + exporter.alias(value)
}

func exportTimestamp(field, value string) bool {
	if !slices.Contains([]string{"time", "timestamp", "deadline", "received", "resumeAt"}, field) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func exportMask(field, value string) bool {
	// net.IPNet serializes masks as base64; they encode prefix lengths only.
	if field == "Mask" {
		return true
	}
	if field != "mask" {
		return false
	}
	ip := net.ParseIP(value).To4()
	if ip == nil {
		return false
	}
	_, bits := net.IPMask(ip).Size()
	return bits != 0
}

func safeExportToken(field, value string) bool {
	var allowed string
	switch field {
	case "type":
		allowed = "started initial sample marker log error failed final completed timeout snapshot"
	case "action":
		allowed = "bound renew deconfig nak leasefail"
	case "proxy":
		allowed = "improxy igmpproxy"
	case "linkState", "link", "loadState", "activeState", "subState", "unitFileState", "snooping", "querier":
		allowed = "up down unknown lowerlayerdown dormant not-found loaded active inactive failed activating deactivating running dead exited enabled disabled static masked"
	case "dhcpRoutes":
		allowed = "none no-default allow-default"
	}
	return slices.Contains(strings.Fields(allowed), value)
}

func (exporter *Exporter) alias(value string) string {
	mac := hmac.New(sha256.New, exporter.key[:])
	_, _ = io.WriteString(mac, value)
	return hex.EncodeToString(mac.Sum(nil)[:12])
}

func (exporter *Exporter) address(address netip.Addr) netip.Addr {
	// Unspecified addresses carry routing semantics, not device identity.
	if address.IsUnspecified() {
		return address
	}
	if alias, found := exporter.addresses[address]; found {
		return alias
	}
	mac := hmac.New(sha256.New, exporter.key[:])
	_, _ = io.WriteString(mac, address.String())
	hash := mac.Sum(nil)
	var alias netip.Addr
	if address.Is4() {
		first := byte(privateAliasOctet)
		if address.IsMulticast() {
			first = 239
		}
		alias = netip.AddrFrom4([4]byte{first, hash[0], hash[1], hash[2]})
	} else {
		var bytes [16]byte
		copy(bytes[:], hash)
		bytes[0] = 0xfd
		if address.IsMulticast() {
			bytes[0], bytes[1] = 0xff, 0x08
		}
		alias = netip.AddrFrom16(bytes)
	}
	for exporter.used[alias] || alias == address {
		alias = alias.Next()
	}
	exporter.addresses[address], exporter.used[alias] = alias, true
	return alias
}

// ExportCapture reads structured captures incrementally; it never modifies input.
// Exported text and JSON derive from exactly the same sanitized event model.
func ExportCapture(input io.Reader, output io.Writer, format string) error {
	exporter, err := NewExporter()
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(input)
	encoder := json.NewEncoder(output)
	for {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read capture event: %w", err)
		}
		clean, err := exporter.Event(event)
		if err != nil {
			return err
		}
		if format == "jsonl" {
			err = encoder.Encode(clean)
		} else {
			_, err = io.WriteString(output, RenderEvent(clean))
		}
		if err != nil {
			return fmt.Errorf("write sanitized export: %w", err)
		}
	}
}
