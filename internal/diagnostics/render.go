package diagnostics

import (
	"fmt"
	"time"
)

// RenderEvent returns a human-readable text-capture line for event.
func RenderEvent(event Event) string {
	switch event.Type {
	case "started":
		return "Share-ready udm-iptv diagnostics. Review before posting publicly.\n" + event.Message + "\n\n"
	case "initial":
		return "=== Initial snapshot ===\n" + RenderSnapshot(*event.Snapshot) + "\n"
	case "sample":
		value := event.Snapshot

		return fmt.Sprintf("[%s] service=%s/%s proxy=%s pid=%d restarts=%d routes=%d multicast=%s\n",
			event.Time.Format(time.RFC3339), value.Service.ActiveState, value.Service.SubState, value.Service.Proxy,
			value.Service.ProxyPID, value.Service.Restarts, len(value.Network.Routes), multicastRouteCount(value.Multicast))
	case "final":
		return "\n=== Final snapshot ===\n" + RenderSnapshot(*event.Snapshot) + "\n"
	case "log":
		return event.Log + "\n"
	case "error":
		return "capture error: " + sanitize(event.Message) + "\n"
	case "completed":
		return "\nCapture completed: " + event.Message + "\n"
	case "timeout":
		return "\nCapture timed out: " + event.Message + "\n"
	default:
		return ""
	}
}
