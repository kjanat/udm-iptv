package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/proxyinventory"
)

// Refresh dependent options on the UI goroutine, not an asynchronous OptionsFunc.
type proxySelect struct {
	*huh.Select[string]

	value   *config.Config
	mld     int
	proxies []proxyinventory.Inventory
}

func newProxySelect(value *config.Config, proxies ...proxyinventory.Inventory) *proxySelect {
	if value.Proxy.MLDVersion != 0 {
		value.Proxy.Program = config.ProxyImproxy
	}
	field := &proxySelect{
		Select: huh.NewSelect[string]().Key("proxy").Title("Multicast proxy").
			Description("IPv6 requires improxy. Unavailable executables cannot be confirmed.").
			Height(len(proxyOptions(0)) + selectChrome).
			Options(detectedProxyOptions(value.Proxy.MLDVersion, proxies)...).Value(&value.Proxy.Program),
		value:   value,
		mld:     value.Proxy.MLDVersion,
		proxies: proxies,
	}
	if len(proxies) > 0 {
		field.Validate(proxies[0].Validate)
	}
	return field
}

func detectedProxyOptions(mld int, proxies []proxyinventory.Inventory) []huh.Option[string] {
	options := proxyOptions(mld)
	if len(proxies) == 0 {
		return options
	}
	for index := range options {
		item := proxies[0].Find(options[index].Value)
		if !item.Available {
			options[index].Key = item.Name + " (unavailable: " + item.Reason + ")"
		} else if item.Source == "offline" {
			options[index].Key = strings.ReplaceAll(options[index].Key, "recommended", "offline runtime")
			if item.Name == config.ProxyIgmpproxy {
				options[index].Key += " — offline runtime"
			}
		}
		if item.Version != "" {
			options[index].Key += " — " + item.Version
		}
	}
	return options
}

func (p *proxySelect) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	if p.mld != p.value.Proxy.MLDVersion {
		p.mld = p.value.Proxy.MLDVersion
		if p.mld != 0 {
			p.value.Proxy.Program = config.ProxyImproxy
			p.Value(&p.value.Proxy.Program)
		}
		p.Options(detectedProxyOptions(p.mld, p.proxies)...)
	}
	_, cmd := p.Select.Update(msg)
	return p, cmd
}
