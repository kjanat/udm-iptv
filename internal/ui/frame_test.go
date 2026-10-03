package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/kjanat/udm-iptv/internal/config/configtest"
)

func TestDHCPOptionsHelpDocumentsInputInPopup(t *testing.T) {
	frame := NewFrame(wizardForm(newPage(huh.NewInput().Key("dhcp-options"))), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	frame.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	view := plain(frame)
	for _, want := range []string{"-O staticroutes -V IPTV_RG", "-H NAME", "quotes do not group words", "not blocked", "udhcpc --help"} {
		if !strings.Contains(view, want) {
			t.Errorf("popup missing %q", want)
		}
	}
	if height := lipgloss.Height(frame.helpBox()); height > 40 {
		t.Errorf("help popup exceeds terminal: %d rows", height)
	}
}

func TestHelpScrollsInsidePopup(t *testing.T) {
	frame := NewFrame(wizardForm(newPage(huh.NewInput().Key("dhcp-options"))), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	frame.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	if height := lipgloss.Height(frame.helpBox()); height > 22 {
		t.Fatalf("popup does not fit: %d rows\n%s", height, frame.helpBox())
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !frame.help || frame.helpOffset == 0 || !strings.Contains(frame.helpBox(), "list.") {
		t.Fatalf("cannot scroll to the end of help:\n%s", frame.helpBox())
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if frame.help || focusedKey(frame.wizard.Form) != "dhcp-options" {
		t.Fatal("closing help changed the question")
	}
}

func TestDHCPHelpRendersOSC8DocumentationLink(t *testing.T) {
	frame := NewFrame(wizardForm(newPage(huh.NewInput().Key("dhcp-options"))), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	frame.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	view := frame.View().Content
	if !strings.Contains(view, "\x1b]8;;"+udhcpcDocumentationURL) {
		t.Fatal("documentation link is not an OSC 8 hyperlink in the rendered popup")
	}
	if !strings.Contains(view, "\x1b]8;;\x07") && !strings.Contains(view, "\x1b]8;;\x1b\\") {
		t.Fatal("documentation hyperlink is not closed")
	}
}

func TestFirstPageIsRecognizedOnEveryField(t *testing.T) {
	value := configtest.Custom()
	fields := newFormValues(value)
	groups, _, _ := configurationGroups(&value, []Port{{Name: "eth8"}}, "", &fields, true)
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
	value := configtest.Custom()
	for _, size := range []tea.WindowSizeMsg{{Width: 10, Height: 5}, {Width: 30, Height: 8}, {Width: 1, Height: 1}} {
		frame := NewFrame(wizardForm(newPage(telemetryConsent(&value.Telemetry))).steps(3, 9), "")
		frame.Init()
		frame.Update(size)
		if view := frame.View().Content; !strings.Contains(view, "Question 4 of 13") {
			t.Fatalf("%dx%d: progress text missing", size.Width, size.Height)
		}
	}
}

func TestEscapeClosesHelpLikeAnyOtherKey(t *testing.T) {
	value := configtest.Custom()
	fields := newFormValues(value)
	groups, _, _ := configurationGroups(&value, []Port{{Name: "eth8"}}, "", &fields, true)
	frame := NewFrame(wizardForm(groups[1:]...).steps(1, 0), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	focusPage(frame, "vlan")
	for _, closing := range []tea.KeyPressMsg{{Code: tea.KeyEscape}, {Code: tea.KeyF1}, {Text: "x", Code: 'x'}} {
		frame.Update(tea.KeyPressMsg{Code: tea.KeyF1})
		if !frame.help {
			t.Fatal("F1 did not open the help")
		}
		frame.Update(closing)
		if frame.help {
			t.Fatalf("%s left the help open", closing)
		}
		if frame.quitPrompt || frame.wizard.wentBack {
			t.Fatalf("%s closed the help and also left the question", closing)
		}
		if focusedKey(frame.wizard.Form) != "vlan" {
			t.Fatalf("%s moved focus to %q", closing, focusedKey(frame.wizard.Form))
		}
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if frame.quitPrompt {
		t.Fatal("the escape that closed the help counted as the first of two")
	}
}
