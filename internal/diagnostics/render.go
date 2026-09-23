package diagnostics

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RenderEvent returns a human-readable text-capture line for event.
func RenderEvent(event Event) string {
	output := renderEvent(event)
	if event.Privacy == PrivacySanitized {
		output = "[sanitized export]\n" + output
		if len(event.AddressOrder) > 0 {
			output += "IPv4 aliases in original numerical order: " + strings.Join(event.AddressOrder, " < ") + "\n"
		}
	}
	return output
}

func renderEvent(event Event) string {
	switch event.Type {
	case EventStarted:
		return "udm-iptv diagnostics [" + event.Privacy + "]\n" + event.Message + "\n\n"
	case EventInitial, "snapshot":
		return "=== Initial snapshot ===\n" + RenderSnapshot(*event.Snapshot) + "\n"
	case EventSample:
		return renderSample(event)
	case EventMarker:
		return "\n>>> " + event.Time.Format(time.RFC3339) + " " + event.Message + "\n\n"
	case EventFinal:
		return "\n=== Final snapshot ===\n" + RenderSnapshot(*event.Snapshot) + "\n"
	case EventLog:
		return renderJournalEntry(event.Time, event.Source, event.Log) + "\n"
	}

	return renderMessage(event)
}

func renderMessage(event Event) string {
	switch event.Type {
	case EventError:
		return "capture error: " + event.Message + "\n"
	case EventCompleted:
		return "\nCapture completed: " + event.Message + "\n"
	case EventTimeout:
		return "\nCapture timed out: " + event.Message + "\n"
	}

	return ""
}

func renderSample(event Event) string {
	value := event.Snapshot

	return fmt.Sprintf("[%s] service=%s/%s proxy=%s pid=%d restarts=%d routes=%d multicast=%s packets=%s\n",
		event.Time.Format(time.RFC3339), value.Service.ActiveState, value.Service.SubState, value.Service.Proxy,
		value.Service.ProxyPID, value.Service.Restarts, len(value.Network.Routes), multicastRouteCount(value.Multicast), multicastPackets(value.Multicast))
}

func multicastPackets(usage *MulticastInfo) string {
	if usage == nil {
		return counterUnavailable
	}

	return strconv.FormatUint(usage.Packets, 10)
}
