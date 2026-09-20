package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
)

func TestPrefixInputsEditIndividualNetworks(t *testing.T) {
	value := "213.75.0.0/16 217.166.0.0/16"
	field := newPrefixInputs(&value)
	frame := NewFrame(wizardForm(newPage(field), newPage(huh.NewInput().Key("next"))), "")
	tm := run(t, frame)
	shown(t, tm, "217.166.0.0/16")
	tm.Send(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	tm.Type("195.121.0.0/16")
	until(t, tm, func(*Frame) bool { return strings.HasSuffix(value, "195.121.0.0/16") })
	press(tm, tea.KeyUp)
	tm.Send(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	until(t, tm, func(*Frame) bool { return value == "213.75.0.0/16 195.121.0.0/16" })
	press(tm, tea.KeyEnter)
	until(t, tm, func(f *Frame) bool { return focusedKey(f.wizard.Form) == "next" })
	finish(t, tm)
}

func TestPrefixInputsRejectMultipleNetworksInOneBox(t *testing.T) {
	value := ""
	field := newPrefixInputs(&value)
	frame := NewFrame(wizardForm(newPage(field), newPage(huh.NewInput().Key("next"))), "")
	tm := run(t, frame)
	tm.Type("10.0.0.0/8 192.0.2.0/24")
	press(tm, tea.KeyEnter)
	shown(t, tm, "network 1:")
	inspect(tm, func(f *Frame) {
		if focusedKey(f.wizard.Form) != "nat" {
			t.Error("invalid list advanced the wizard")
		}
		if count := strings.Count(plain(f), "network 1:"); count != 1 {
			t.Errorf("validation error rendered %d times", count)
		}
	})
	tm.Send(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	press(tm, tea.KeyEnter)
	until(t, tm, func(f *Frame) bool { return focusedKey(f.wizard.Form) == "next" })
	finish(t, tm)
	if value != "" {
		t.Fatalf("removing final network left %q", value)
	}
}

func TestNATNetworkValidationExplainsFormatAndIPv6(t *testing.T) {
	for _, value := range []string{"195.121.0", "195.121.0.0", "bad"} {
		err := validateNATNetwork(value)
		if err == nil || !strings.Contains(err.Error(), "195.121.0.0/16") || !strings.Contains(err.Error(), "do not guess") {
			t.Errorf("%q lacks actionable format guidance: %v", value, err)
		}
	}
	if err := validateNATNetwork("2001:db8::/32"); err == nil || !strings.Contains(err.Error(), "IPv6 multicast") {
		t.Errorf("IPv6 limitation not explained: %v", err)
	}
	for _, value := range []string{"", "195.121.0.0/16", "148.122.7.125/32", "0.0.0.0/0"} {
		if err := validateNATNetwork(value); err != nil {
			t.Errorf("valid provider network %q rejected: %v", value, err)
		}
	}
}

func TestQuestionMarkOpensHelpWithoutFiltering(t *testing.T) {
	frame, _ := countryFrame(t)
	tm := run(t, frame)
	tm.Type("?")
	until(t, tm, func(f *Frame) bool { return f.help })
	inspect(tm, func(f *Frame) {
		view := plain(f)
		if !strings.Contains(view, "╔") || !strings.Contains(view, "Question") {
			t.Error("help did not overlay the existing question")
		}
	})
	press(tm, tea.KeyEscape)
	until(t, tm, func(f *Frame) bool { return !f.help })
	finish(t, tm)
}

func TestIPv6ChoicePrecedesAndRestrictsProxy(t *testing.T) {
	value := configtest.KPN()
	value.Proxy.Program = config.ProxyIgmpproxy
	fields := newFormValues(value)
	pages := multicastPages(&value, &fields)
	frame := NewFrame(wizardForm(pages[1]), "")
	tm := run(t, frame)
	until(t, tm, func(f *Frame) bool { return focusedKey(f.wizard.Form) == "mld" })
	press(tm, tea.KeyDown, tea.KeyEnter)
	until(t, tm, func(f *Frame) bool {
		return focusedKey(f.wizard.Form) == "proxy" && value.Proxy.MLDVersion == config.MaxMLDVersion && value.Proxy.Program == config.ProxyImproxy
	})
	press(tm, tea.KeyDown)
	inspect(tm, func(*Frame) {
		if value.Proxy.Program != config.ProxyImproxy {
			t.Error("IPv6 allowed igmpproxy")
		}
	})
	finish(t, tm)
}
