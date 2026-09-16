package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/kjanat/udm-iptv/internal/config"
)

func TestFirstPageIsRecognizedOnEveryField(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups, _, _ := configurationGroups(&value, []Port{{Name: "eth8"}}, "", &fields)
	frame := NewFrame(wizardForm(groups[1:]...).steps(1, 0), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	focusPage(frame, "vlan")
	frame.Update(huh.NextField())
	if focusedKey(frame.wizard.Form) != "dhcp" {
		t.Fatalf("focus did not move to the second field, focused %q", focusedKey(frame.wizard.Form))
	}
	if !frame.wizard.onFirstPage() {
		t.Fatal("second field of the first page not recognized as the first page")
	}
	if p, ok := frame.wizard.focusedPage(); !ok || p.keys[0] != "vlan" {
		t.Fatal("focused page not found from its second field")
	}
	_, cmd := frame.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	frame.Update(cmd())
	if focusedKey(frame.wizard.Form) != "vlan" || frame.wizard.wentBack {
		t.Fatal("escape on the second field must step to the first field, not out of the form")
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !frame.quitPrompt || frame.wizard.wentBack {
		t.Fatal("a second escape in a row must offer to leave")
	}
	frame.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !frame.wizard.wentBack {
		t.Fatal("escape on the first field of the first page must step back a form")
	}
}

func TestNarrowTerminalDoesNotPanic(t *testing.T) {
	value := config.Default()
	for _, size := range []tea.WindowSizeMsg{{Width: 10, Height: 5}, {Width: 30, Height: 8}, {Width: 1, Height: 1}} {
		frame := NewFrame(wizardForm(newPage(telemetryConsent(&value.Telemetry))).steps(3, 9), "")
		frame.Init()
		frame.Update(size)
		if view := frame.View().Content; !strings.Contains(view, "Question 4 of 13") {
			t.Fatalf("%dx%d: progress text missing", size.Width, size.Height)
		}
	}
}
