package ui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

func TestWANPortSelection(t *testing.T) {
	current := "eth8"
	groups, selected := wanGroups(&current, []Port{
		{Name: "eth8", Description: "connected, Internet route"},
		{Name: "eth9", Description: "disconnected"},
	})
	form := wizardForm(groups...).Form
	field := form.GetFocusedField()
	field.Focus()
	field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if *selected != "eth9" || current != "eth8" {
		t.Fatal("selection must remain a draft")
	}
	form.NextGroup()
	if form.State != huh.StateCompleted {
		t.Fatal("selected port asked for manual name")
	}

	groups, selected = wanGroups(&current, nil)
	if len(groups) != 1 {
		t.Fatalf("manual entry became a page: %d pages", len(groups))
	}
	form = wizardForm(groups...).Form
	field = form.GetFocusedField()
	field.Focus()
	field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if *selected != manualPort {
		t.Fatal("manual fallback unavailable")
	}
	field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(form.Errors()) == 0 {
		t.Fatal("the manual row must not pass as a port name")
	}
}

func TestWANPortLabels(t *testing.T) {
	current := "eth8"
	groups, _ := wanGroups(&current, []Port{{Name: "eth8", Description: "example: connected, Internet route", Addresses: []string{"203.0.113.10/24"}, AddressesKnown: true}})
	form := wizardForm(groups...).Form
	form.Init()
	form.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	view := form.View()
	for _, text := range []string{"Which connection", "203.0.113.10/24", "Internet route", "manually"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing guidance %q", text)
		}
	}
}

func TestLANNetworkSelection(t *testing.T) {
	groups, selected := lanGroups([]string{"br0"}, []Port{
		{Name: "eth8", Description: "connected, Internet route", Addresses: []string{"203.0.113.10/24"}, AddressesKnown: true},
		{Name: "br0", Addresses: []string{"192.168.1.1/24"}, AddressesKnown: true},
		{Name: "br4", Addresses: []string{"192.168.4.1/24"}, AddressesKnown: true},
		{Name: "eth0.10", Addresses: []string{"10.0.10.1/24"}, AddressesKnown: true},
	})
	form := wizardForm(groups...).Form
	form.Init()
	form.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	view := form.View()
	for _, text := range []string{"Which networks", "br0", "LAN", "192.168.1.1/24", "VLAN 4", "manually"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing guidance %q in %s", text, view)
		}
	}
	if strings.Contains(view, "eth8") {
		t.Fatal("WAN port listed as a TV network")
	}
	if !reflect.DeepEqual(*selected, []string{"br0"}) {
		t.Fatalf("selection = %v", *selected)
	}
	form.NextGroup()
	if form.State != huh.StateCompleted {
		t.Fatal("selected networks asked for manual names")
	}

	groups, _ = lanGroups(nil, nil)
	if len(groups) != 1 {
		t.Fatalf("manual entry became a page: %d pages", len(groups))
	}
	form = wizardForm(groups...).Form
	field := form.GetFocusedField()
	field.Focus()
	field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(form.Errors()) == 0 {
		t.Fatal("empty selection passed")
	}
	if got := resolveLAN([]string{manualPort, "br4", "br4"}); !reflect.DeepEqual(got, []string{"br4"}) {
		t.Fatalf("resolved %v", got)
	}
}

func TestLANKindLabels(t *testing.T) {
	for _, test := range []struct {
		port Port
		want string
	}{
		{Port{Name: "br0", AddressesKnown: true, Addresses: []string{"192.168.1.1/24"}}, "br0 (LAN, 192.168.1.1/24)"},
		{Port{Name: "br4", AddressesKnown: true, Addresses: []string{"192.168.4.1/24"}}, "br4 (VLAN 4, 192.168.4.1/24)"},
		{Port{Name: "eth0.10", AddressesKnown: true, Addresses: []string{"10.0.10.1/24"}}, "eth0.10 (VLAN 10, 10.0.10.1/24)"},
	} {
		if got := test.port.lanLabel(); got != test.want {
			t.Fatalf("got %q, want %q", got, test.want)
		}
	}
}

func TestPortAddressLabels(t *testing.T) {
	for _, test := range []struct {
		port Port
		want string
	}{
		{Port{Name: "eth8", AddressesKnown: true}, "eth8 (no assigned IP)"},
		{Port{Name: "eth9"}, "eth9 (addresses unavailable)"},
		{Port{Name: "br0", AddressesKnown: true, Addresses: []string{"192.168.1.1/24", "2001:db8::1/64"}}, "br0 (192.168.1.1/24, 2001:db8::1/64)"},
	} {
		if got := test.port.label(); got != test.want {
			t.Fatalf("got %q, want %q", got, test.want)
		}
	}
}
