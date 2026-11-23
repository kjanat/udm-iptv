package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

func TestDHCPNoOpensStaticAddressPopup(t *testing.T) {
	enabled, address := true, ""
	frame := NewFrame(wizardForm(newPage(newDHCPConfirm(&enabled, &address)), newPage(huh.NewInput().Key("next"))), "")
	tm := run(t, frame)
	tm.Type("n")
	until(t, tm, func(f *Frame) bool { return f.entry != nil })
	inspect(tm, func(f *Frame) {
		if focusedKey(f.wizard.Form) != "dhcp" || !strings.Contains(plain(f), "enter save") {
			t.Error("static address is not a popup over the DHCP question")
		}
	})
	tm.Type("invalid")
	press(tm, tea.KeyEnter)
	until(t, tm, func(f *Frame) bool { return f.entryErr != nil })
	inspect(tm, func(*Frame) {
		if address != "" {
			t.Error("invalid input was saved")
		}
	})
	for range len("invalid") {
		press(tm, tea.KeyBackspace)
	}
	tm.Type("10.0.0.2/24")
	press(tm, tea.KeyEnter)
	until(t, tm, func(f *Frame) bool { return f.entry == nil && focusedKey(f.wizard.Form) == "next" })
	finish(t, tm)
	if enabled || address != "10.0.0.2/24" {
		t.Fatalf("DHCP=%t, address=%q", enabled, address)
	}
}

func TestStaticAddressPopupCancelPreservesAddress(t *testing.T) {
	enabled, address := false, "10.0.0.2/24"
	frame := NewFrame(wizardForm(newPage(newDHCPConfirm(&enabled, &address)), newPage(huh.NewInput().Key("next"))), "")
	tm := run(t, frame)
	press(tm, tea.KeyEnter)
	until(t, tm, func(f *Frame) bool { return f.entry != nil && f.entryText == address })
	tm.Type("9")
	press(tm, tea.KeyEscape)
	until(t, tm, func(f *Frame) bool { return f.entry == nil && focusedKey(f.wizard.Form) == "dhcp" })
	tm.Type("y")
	until(t, tm, func(f *Frame) bool { return f.entry == nil && focusedKey(f.wizard.Form) == "next" })
	finish(t, tm)
	if !enabled || address != "10.0.0.2/24" {
		t.Fatalf("cancel changed address: DHCP=%t, address=%q", enabled, address)
	}
}
