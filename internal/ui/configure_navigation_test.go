package ui

import (
	"context"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/config/configtest"
)

func selectSecondLAN(t *testing.T, wizard *Wizard) {
	t.Helper()
	frame := NewFrame(wizard, "")
	frame.Init()
	focusPage(frame, "lan")
	field := frame.wizard.Form.GetFocusedField()
	field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	field.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	selected, ok := field.GetValue().([]string)
	if !ok || !slices.Contains(selected, "br2") {
		t.Fatal("test did not select br2")
	}
	// Real back navigation leaves focus on the first page.
	for focusedKey(wizard.Form) != "wan-port" {
		wizard.Form.PrevGroup()
	}
}

func TestProviderBackRetainsUnfinishedInput(t *testing.T) {
	value := configtest.KPN()
	runs := 0
	run := func(_ context.Context, wizard *Wizard) error {
		if focusedKey(wizard.Form) != "wan-port" {
			return nil
		}
		runs++
		frame := NewFrame(wizard, "")
		frame.Init()
		focusPage(frame, "vlan")
		field := wizard.Form.GetFocusedField()
		if runs == 1 {
			field.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
			for focusedKey(wizard.Form) != "wan-port" {
				wizard.Form.PrevGroup()
			}
			return ErrBack
		}
		if field.GetValue() != "" {
			t.Fatalf("unfinished VLAN input was reset to %v", field.GetValue())
		}
		field.Update(tea.KeyPressMsg{Code: '4', Text: "4"})
		return nil
	}
	if err := ConfigureSuggested(t.Context(), &value, config.DefaultCatalog(), run, "", nil, Port{Name: "eth8"}, Port{Name: "br0"}); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || value.WAN.VLAN != 4 {
		t.Fatalf("runs=%d VLAN=%d", runs, value.WAN.VLAN)
	}
}

func TestConfigurePreservesLANEditsAcrossNavigation(t *testing.T) {
	for _, navigation := range []string{"forward", "back-to-provider", "back-from-review"} {
		t.Run(navigation, func(t *testing.T) {
			value := configtest.KPN()
			value.LAN.Interfaces = []string{"br0"}
			settingsRuns, reviewRuns := 0, 0
			run := func(_ context.Context, wizard *Wizard) error {
				switch focusedKey(wizard.Form) {
				case "wan-port":
					settingsRuns++
					if settingsRuns == 1 {
						selectSecondLAN(t, wizard)
						if navigation == "back-to-provider" {
							return ErrBack
						}
					}
				case "accept":
					reviewRuns++
					if navigation == "back-from-review" && reviewRuns == 1 {
						return ErrBack
					}
				}
				return nil
			}
			err := ConfigureSuggested(t.Context(), &value, config.DefaultCatalog(), run, "", nil,
				Port{Name: "eth8"}, Port{Name: "br0"}, Port{Name: "br2"})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(value.LAN.Interfaces, []string{"br0", "br2"}) {
				t.Fatalf("saved %v after selecting br2", value.LAN.Interfaces)
			}
		})
	}
}
