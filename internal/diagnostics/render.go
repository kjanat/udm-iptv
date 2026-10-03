package diagnostics

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RenderEvent returns a human-readable text-capture line for event.
func RenderEvent(event Event) string {
	output := renderEvent(event) + renderEventDetails(event)
	if event.Privacy == PrivacySanitized {
		output = "[sanitized export]\n" + output
		if len(event.AddressOrder) > 0 {
			output += "IPv4 aliases in original numerical order: " + strings.Join(event.AddressOrder, " < ") + "\n"
		}
	}
	return output
}

func renderEventDetails(event Event) string {
	var output strings.Builder
	if snapshotEvent(event.Type) && event.Message != "" {
		fmt.Fprintf(&output, "Event message: %s\n", event.Message)
	}
	if event.Type != EventLog && event.Log != "" {
		output.WriteString(renderJournalEntry(event.Time, event.Source, event.Log) + "\n")
	}
	if len(event.Journal) > 0 {
		data, err := json.Marshal(event.Journal)
		if err != nil {
			fmt.Fprintf(&output, "Journal metadata encoding failed: %s\n", err)
		} else {
			fmt.Fprintf(&output, "Journal record: %s\n", data)
		}
	}
	return output.String()
}

func snapshotEvent(kind string) bool {
	switch kind {
	case EventInitial, EventSample, EventFinal, "snapshot":
		return true
	}
	return false
}

func renderEvent(event Event) string {
	switch event.Type {
	case EventStarted:
		return "udm-iptv diagnostics [" + event.Privacy + "]\nStarted at: " + event.Time.Format(time.RFC3339Nano) + "\n" + event.Message + "\n\n"
	case EventInitial, "snapshot":
		return "=== Initial snapshot ===\n" + RenderSnapshot(*event.Snapshot) + "\n"
	case EventSample:
		return renderSample(event)
	case EventMarker:
		return "\n>>> " + event.Time.Format(time.RFC3339Nano) + " " + event.Message + "\n\n"
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
	case EventFailed:
		return "Capture failed: " + event.Message + "\n"
	case EventCompleted:
		return "\nCapture completed: " + event.Message + "\n"
	case EventTimeout:
		return "\nCapture timed out: " + event.Message + "\n"
	}

	return event.Type + ": " + event.Message + "\n"
}

func renderSample(event Event) string {
	value := event.Snapshot

	return fmt.Sprintf("[%s] service=%s/%s proxy=%s pid=%d restarts=%s routes=%s multicast=%s packets=%s\n",
		event.Time.Format(time.RFC3339Nano), value.Service.ActiveState, value.Service.SubState, value.Service.Proxy,
		value.Service.ProxyPID, observedText(value.Service.Errors, strconv.FormatUint(value.Service.Restarts, 10), "systemd"), observedText(value.Network.Errors, strconv.Itoa(len(value.Network.Routes)), "link", "routes"), multicastRouteCount(value.Multicast), multicastPackets(value.Multicast)) + RenderSnapshot(*value)
}

func multicastPackets(usage *MulticastInfo) string {
	if usage == nil {
		return counterUnavailable
	}

	return strconv.FormatUint(usage.Packets, 10)
}
