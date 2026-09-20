package ui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/kjanat/udm-iptv/internal/config"
)

// Refresh dependent options on the UI goroutine, not an asynchronous OptionsFunc.
type proxySelect struct {
	*huh.Select[string]

	value *config.Config
	mld   int
}

func newProxySelect(value *config.Config) *proxySelect {
	if value.Proxy.MLDVersion != 0 {
		value.Proxy.Program = config.ProxyImproxy
	}
	return &proxySelect{
		Select: huh.NewSelect[string]().Key("proxy").Title("Multicast proxy").
			Description("improxy supports IPv4 and IPv6. igmpproxy supports IPv4 only.").
			Height(len(proxyOptions(0)) + selectChrome).
			Options(proxyOptions(value.Proxy.MLDVersion)...).Value(&value.Proxy.Program),
		value: value,
		mld:   value.Proxy.MLDVersion,
	}
}

func (p *proxySelect) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	if p.mld != p.value.Proxy.MLDVersion {
		p.mld = p.value.Proxy.MLDVersion
		if p.mld != 0 {
			p.value.Proxy.Program = config.ProxyImproxy
			p.Value(&p.value.Proxy.Program)
		}
		p.Options(proxyOptions(p.mld)...)
	}
	_, cmd := p.Select.Update(msg)
	return p, cmd
}
