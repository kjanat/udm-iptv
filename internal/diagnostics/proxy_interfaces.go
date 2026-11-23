package diagnostics

import (
	"strings"

	"github.com/kjanat/udm-iptv/internal/service"
)

func (r reportRenderer) proxyInterfaces(state *service.ProxyInterfaces) string {
	if state == nil {
		return r.field("Proxy interface registration", r.text(ReportWarning, "unavailable (not collected)"))
	}
	verdict := "all configured interfaces registered (not a playback check)"
	role := ReportHeading
	if err := state.Check(); err != nil {
		verdict = "WARNING: " + err.Error()
		role = ReportWarning
	}
	var output strings.Builder
	output.WriteString(r.field("Proxy interface registration", r.text(role, verdict)))
	output.WriteString("Kernel IPv4 multicast VIF table:\n" + state.IPv4.Raw)
	if state.IPv4.Error != "" {
		output.WriteString(r.text(ReportWarning, state.IPv4.Error) + "\n")
	}
	if state.IPv6 != nil {
		output.WriteString("Kernel IPv6 multicast MIF table:\n" + state.IPv6.Raw)
		if state.IPv6.Error != "" {
			output.WriteString(r.text(ReportWarning, state.IPv6.Error) + "\n")
		}
	}
	return output.String()
}
