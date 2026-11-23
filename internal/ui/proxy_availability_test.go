package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
	"github.com/kjanat/udm-iptv/internal/proxyinventory"
)

func TestUnavailableProxyIsMarkedAndCannotBeConfirmed(t *testing.T) {
	value := configtest.KPN()
	value.Proxy.Program = config.ProxyIgmpproxy
	inventory := proxyinventory.Inventory{
		{Name: config.ProxyImproxy, Available: true, Source: "system", Version: "0.3"},
		{Name: config.ProxyIgmpproxy, Source: "missing", Reason: "not installed"},
	}
	field := newProxySelect(&value, inventory)
	frame := NewFrame(wizardForm(newPage(field), newPage(huh.NewInput().Key("next"))), "")
	tm := run(t, frame)
	shown(t, tm, "unavailable: not installed")
	press(tm, tea.KeyEnter)
	shown(t, tm, "igmpproxy unavailable")
	inspect(tm, func(f *Frame) {
		if focusedKey(f.wizard.Form) != "proxy" {
			t.Error("unavailable proxy was accepted")
		}
	})
	press(tm, tea.KeyUp, tea.KeyEnter)
	until(t, tm, func(f *Frame) bool { return focusedKey(f.wizard.Form) == "next" })
	finish(t, tm)
	if value.Proxy.Program != config.ProxyImproxy {
		t.Fatal("available proxy selection failed")
	}
}

func TestOfflineProxyRemainsSelectable(t *testing.T) {
	inventory := proxyinventory.Inventory{
		{Name: config.ProxyImproxy, Source: "missing", Reason: "not installed"},
		{Name: config.ProxyIgmpproxy, Available: true, Source: "offline", Version: "0.4"},
	}
	if err := inventory.Validate(config.ProxyIgmpproxy); err != nil {
		t.Fatal(err)
	}
	options := detectedProxyOptions(0, []proxyinventory.Inventory{inventory})
	if options[1].Key != "igmpproxy (IPv4 only) — offline runtime — 0.4" {
		t.Fatalf("offline label = %q", options[1].Key)
	}
	if options := detectedProxyOptions(2, []proxyinventory.Inventory{inventory}); len(options) != 1 || options[0].Value != config.ProxyImproxy {
		t.Fatal("IPv6 allowed an IPv4-only proxy")
	}
}
