package ui

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/kjanat/udm-iptv/internal/config"
)

func TestConfigureProfileSwitch(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "accept", true: "cancel"}[cancel], func(t *testing.T) {
			value := config.Default()
			value.Telemetry.Enabled = true
			original := clone(value)
			selected, _ := config.FromProfile("tweak", value)
			selected.WAN.Interface = "example9"
			profiles := []config.Profile{
				{ID: "kpn", Name: "KPN", Config: value},
				{ID: "tweak", Name: "Tweak", Config: selected},
			}
			calls := 0
			aborted := errors.New("cancelled")
			err := Configure(context.Background(), &value, profiles, func(_ context.Context, wizard *Wizard) error {
				form := wizard.Form
				calls++
				if calls == 1 {
					field := form.GetFocusedField()
					field.Focus()
					_, _ = field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
					_, _ = field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				} else if cancel {
					return aborted
				}

				return nil
			})
			wantCalls := 3
			if cancel {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("forms = %d", calls)
			}
			if cancel {
				if !errors.Is(err, aborted) || !reflect.DeepEqual(value, original) {
					t.Fatalf("cancel modified input: %v", err)
				}

				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if value.Profile != "tweak" || value.WAN.Interface != "example9" || !value.Telemetry.Enabled {
				t.Fatalf("wrong selection: %+v", value)
			}
			value.WAN.NATDestinations[0] = "changed"
			if selected.WAN.NATDestinations[0] != "0.0.0.0/0" {
				t.Fatal("profile data aliased")
			}
		})
	}
}

func TestConfigurationPagesFollowAnswers(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*config.Config)
		want []string
	}{
		{
			name: "kpn",
			want: []string{"wan-port", "vlan", "vlan-interface", "dhcp-options", "lan", "nat", "proxy", "telemetry"},
		},
		{
			name: "untagged-static-igmpproxy",
			edit: func(value *config.Config) {
				value.WAN.VLAN = 0
				value.WAN.DHCP = false
				value.WAN.StaticAddress = "10.20.30.1/24"
				value.Proxy.Program = "igmpproxy"
			},
			want: []string{"wan-port", "vlan", "static-address", "lan", "nat", "proxy", "proxy-sources", "telemetry"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := config.Default()
			if test.edit != nil {
				test.edit(&value)
			}
			fields := newFormValues(value)
			groups := configurationPages(&value, nil, "", &fields)
			form := wizardForm(groups...).Form
			var keys []string
			for range 20 {
				if form.State == huh.StateCompleted {
					break
				}
				field := form.GetFocusedField()
				if field == nil {
					t.Fatal("no focused field")
				}
				keys = append(keys, field.GetKey())
				form.NextGroup()
			}
			if !reflect.DeepEqual(keys, test.want) {
				t.Fatalf("pages = %v, want %v", keys, test.want)
			}
		})
	}
}

func TestConfigurationPageFits(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups := configurationPages(&value, []Port{
		{Name: "eth8", Description: "connected, Internet route", Addresses: []string{"203.0.113.10/24"}, AddressesKnown: true},
		{Name: "br0", Description: "example: LAN", Addresses: []string{"192.168.1.1/24"}, AddressesKnown: true},
		{Name: "br4", Addresses: []string{"192.168.4.1/24"}, AddressesKnown: true},
	}, "IPTV DNS servers: 177.16.30.67 and 177.16.30.7.", &fields)
	frame := NewFrame(wizardForm(groups...), "Preview · Example data.")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 180, Height: 45})
	boxRows := 0
	for range 12 {
		if frame.wizard.Form.State == huh.StateCompleted {
			return
		}
		view := frame.View()
		if !view.AltScreen {
			t.Fatal("wizard left the alternate screen")
		}
		lines := strings.Split(view.Content, "\n")
		if len(lines) != 45 || lipgloss.Width(view.Content) != 180 {
			t.Fatalf("page is %dx%d, terminal is 180x45", lipgloss.Width(view.Content), len(lines))
		}
		top, bottom, left := -1, -1, -1
		for i, line := range lines {
			switch {
			case strings.Contains(line, "╭"):
				top, left = i, len(line)-len(strings.TrimLeft(line, " "))
			case strings.Contains(line, "╰"):
				bottom = i
			}
		}
		if top < 0 || bottom < 0 {
			t.Fatal("page has no box")
		}
		if above, below := top-3, 44-bottom; above < 3 || below < 3 || above-below > 2 || below-above > 2 {
			t.Fatalf("box is not vertically centered: %d rows above the header, %d below", above, below)
		}
		right := 180 - lipgloss.Width(strings.TrimRight(lines[top], " "))
		if left < 20 || right < 20 || left-right > 2 || right-left > 2 {
			t.Fatalf("box is not horizontally centered: %d left, %d right", left, right)
		}
		if boxRows == 0 {
			boxRows = bottom - top
		}
		if bottom-top != boxRows {
			t.Fatalf("box height changed between pages: %d rows, then %d", boxRows, bottom-top)
		}
		if !strings.Contains(lines[top-3], "Preview · Example data.") {
			t.Fatalf("header missing above the box: %q", lines[top-3])
		}
		for _, jammed := range []string{"quickleave?Off", "logs?Temporary", "address?Most"} {
			if strings.Contains(view.Content, jammed) {
				t.Fatalf("confirm title ate its description: %q", jammed)
			}
		}
		frame.wizard.Form.NextGroup()
	}
	t.Fatal("form did not finish")
}

func TestConfigureFailureDoesNotChangeInput(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		value := config.Default()
		value.WAN.VLAN = 5000
		original := clone(value)
		calls := 0
		err := Configure(context.Background(), &value, config.Profiles(), func(_ context.Context, _ *Wizard) error {
			calls++
			if failAt == 1 {
				return huh.ErrUserAborted
			}

			return nil
		})
		if err == nil || !reflect.DeepEqual(value, original) {
			t.Fatal("failure must leave input untouched")
		}
	}
}

func TestFrameKeepsBoxHeightAcrossForms(t *testing.T) {
	settings := config.Default()
	fields := newFormValues(settings)
	groups := configurationPages(&settings, nil, "", &fields)
	frame := NewFrame(wizardForm(newPage(telemetryConsent(&settings.Telemetry))), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	first := lipgloss.Height(frame.View().Content)
	frame.Update(setWizardMsg{wizard: wizardForm(groups...)})
	tall := boxRows(frame.View().Content)
	frame.Update(setWizardMsg{wizard: wizardForm(newPage(telemetryConsent(&settings.Telemetry)))})
	if first != 40 || boxRows(frame.View().Content) != tall {
		t.Fatalf("box shrank after a tall form: %d, then %d", tall, boxRows(frame.View().Content))
	}
}

func boxRows(content string) int {
	top, bottom := -1, -1
	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "╭") {
			top = i
		}
		if strings.Contains(line, "╰") {
			bottom = i
		}
	}

	return bottom - top
}

func TestEveryQuestionHasHelp(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups := configurationPages(&value, []Port{{Name: "eth8"}}, "", &fields)
	keys := []string{"profile", "accept"}
	for _, p := range groups {
		keys = append(keys, p.keys...)
	}
	for _, key := range keys {
		entry, ok := fieldHelp[key]
		if !ok || entry.title == "" || len(strings.Fields(entry.text)) < 15 {
			t.Errorf("question %q has no plain-language help", key)
		}
	}
	for key := range fieldHelp {
		if !slices.Contains(keys, key) {
			t.Errorf("help for unknown question %q", key)
		}
	}
}

func TestHelpOverlayExplainsFocusedQuestion(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups := configurationPages(&value, nil, "", &fields)
	frame := NewFrame(wizardForm(groups...).steps(1, 1), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	for focusedKey(frame.wizard.Form) != "vlan" {
		frame.wizard.Form.NextGroup()
	}
	frame.wizard.Form.GetFocusedField().Focus()
	if view := frame.View().Content; !strings.Contains(view, "Question 3 of") || !strings.Contains(view, "F1") {
		t.Fatalf("progress line missing question counter or help hint")
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	view := frame.View().Content
	if !strings.Contains(view, "IPTV VLAN ID") || !strings.Contains(view, "separate numbered lane") {
		t.Fatal("help overlay does not explain the VLAN question")
	}
	frame.Update(tea.KeyPressMsg{Text: "9", Code: '9'})
	if fields.vlan != "4" {
		t.Fatal("keypress that closed the help reached the field")
	}
	if strings.Contains(frame.View().Content, "separate numbered lane") {
		t.Fatal("help overlay did not close")
	}
}

func TestEnterOnManualNetworkEntryTicksIt(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups, selectedPort, selectedLAN, _ := configurationGroups(&value, []Port{{Name: "br0", AddressesKnown: true}}, "", &fields)
	*selectedPort = "eth8"
	frame := NewFrame(wizardForm(groups...), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	for focusedKey(frame.wizard.Form) != "lan" {
		frame.wizard.Form.NextGroup()
	}
	frame.wizard.Form.GetFocusedField().Focus()
	frame.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if containsString(*selectedLAN, manualPort) {
		t.Fatal("manual entry ticked before enter")
	}
	_, cmd := frame.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !containsString(*selectedLAN, manualPort) || !containsString(*selectedLAN, "br0") || cmd == nil {
		t.Fatalf("enter on the manual row: selected=%v cmd=%v", *selectedLAN, cmd != nil)
	}
	_, cmd = frame.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !containsString(*selectedLAN, manualPort) || cmd == nil {
		t.Fatalf("second enter changed the tick: %v", *selectedLAN)
	}
}

func TestCtrlCAsksBeforeLeaving(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups, _, _, _ := configurationGroups(&value, nil, "", &fields)
	var events []string
	frame := NewFrame(wizardForm(groups...), "")
	frame.observer = func(event, question string) { events = append(events, event+":"+question) }
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	frame.Update(ctrlC)
	view := frame.View().Content
	if !strings.Contains(view, "Leave the wizard?") || !strings.Contains(view, "Which connection") || frame.wizard.Form.State != huh.StateNormal {
		t.Fatal("first ctrl+c did not open the popup over the question")
	}
	frame.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	if strings.Contains(frame.View().Content, "Leave the wizard?") || frame.wizard.Form.State != huh.StateNormal {
		t.Fatal("n did not dismiss the popup")
	}
	frame.Update(ctrlC)
	frame.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if strings.Contains(frame.View().Content, "Leave the wizard?") || frame.wizard.Form.State != huh.StateNormal {
		t.Fatal("enter on No, stay did not stay")
	}
	frame.Update(ctrlC)
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if frame.wizard.Form.State != huh.StateAborted {
		t.Fatal("enter on Yes, leave did not leave")
	}
	want := []string{"quit.prompt:wan-port", "quit.prompt:wan-port", "quit.prompt:wan-port", "abort:wan-port"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestEscapeLeavesUnlessFiltering(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups, _, _, _ := configurationGroups(&value, []Port{{Name: "eth8"}, {Name: "eth9"}}, "", &fields)
	frame := NewFrame(wizardForm(groups...), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	frame.wizard.Form.GetFocusedField().Focus()
	frame.Update(tea.KeyPressMsg{Text: "/", Code: '/'})
	frame.Update(tea.KeyPressMsg{Text: "9", Code: '9'})
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if strings.Contains(frame.View().Content, "Leave the wizard?") {
		t.Fatal("escape while filtering asked to leave")
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !strings.Contains(frame.View().Content, "Leave the wizard?") {
		t.Fatal("escape did not prompt")
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if strings.Contains(frame.View().Content, "Leave the wizard?") || frame.wizard.Form.State != huh.StateNormal {
		t.Fatal("escape did not close the popup")
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	frame.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if frame.wizard.Form.State != huh.StateAborted {
		t.Fatal("ctrl+c in the popup did not leave")
	}
}

func TestCloseButtonAndPopupButtonsAreClickable(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups, _, _, _ := configurationGroups(&value, nil, "", &fields)
	frame := NewFrame(wizardForm(groups...), "Preview")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	view := frame.View()
	if view.MouseMode == tea.MouseModeNone {
		t.Fatal("mouse disabled")
	}
	x, y := locate(view.Content, closeLabel)
	if x < 60 || y < 0 {
		t.Fatalf("close button not on the right: %d,%d", x, y)
	}
	frame.Update(tea.MouseClickMsg{X: x - 1, Y: y, Button: tea.MouseLeft})
	if strings.Contains(frame.View().Content, "Leave the wizard?") {
		t.Fatal("click beside the button prompted")
	}
	frame.Update(tea.MouseClickMsg{X: x + 2, Y: y, Button: tea.MouseLeft})
	content := frame.View().Content
	if !strings.Contains(content, "Leave the wizard?") {
		t.Fatal("click on the button did not open the popup")
	}
	stayX, stayY := locate(content, stayLabel)
	frame.Update(tea.MouseClickMsg{X: stayX + 1, Y: stayY, Button: tea.MouseLeft})
	if strings.Contains(frame.View().Content, "Leave the wizard?") {
		t.Fatal("click on No, stay did not close the popup")
	}
	frame.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	leaveX, leaveY := locate(frame.View().Content, leaveLabel)
	frame.Update(tea.MouseClickMsg{X: leaveX + 1, Y: leaveY, Button: tea.MouseLeft})
	if frame.wizard.Form.State != huh.StateAborted {
		t.Fatal("click on Yes, leave did not leave")
	}
}

func TestVLANFieldAcceptsDigitsOnly(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups := configurationPages(&value, nil, "", &fields)
	frame := NewFrame(wizardForm(groups...), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	for focusedKey(frame.wizard.Form) != "vlan" {
		frame.wizard.Form.NextGroup()
	}
	frame.wizard.Form.GetFocusedField().Focus()
	for _, msg := range []tea.KeyPressMsg{{Text: "s", Code: 's'}, {Text: "-", Code: '-'}, {Text: " ", Code: tea.KeySpace}} {
		frame.Update(msg)
		if fields.vlan != "4" {
			t.Fatalf("non-digit %q reached the VLAN field: %q", msg.Text, fields.vlan)
		}
	}
	frame.Update(tea.KeyPressMsg{Text: "0", Code: '0'})
	if fields.vlan != "40" {
		t.Fatalf("digit dropped on the VLAN field: %q", fields.vlan)
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if fields.vlan != "4" {
		t.Fatalf("backspace dropped on the VLAN field: %q", fields.vlan)
	}
	if _, cmd := frame.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil {
		t.Fatal("enter dropped on the VLAN field")
	}
}
