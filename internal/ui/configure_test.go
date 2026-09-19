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

var errProfileSwitchAborted = errors.New("cancelled")

func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

type profileSwitchResult struct {
	value, before, selected config.Config
	err                     error
	calls                   int
}

func twoProfileCatalog(t *testing.T) (config.Config, config.Config, config.Catalog) {
	t.Helper()
	value := config.DefaultKPN()
	value.Telemetry.Enabled = true
	selected, err := config.FromProfile("tweak", value)
	if err != nil {
		t.Fatalf("FromProfile(tweak): %v", err)
	}
	selected.WAN.Interface = "example9"

	return value, selected, config.Catalog{
		Countries: []config.Country{{Code: "NL", Name: "Netherlands", LocalName: "Nederland"}},
		Providers: []config.Provider{
			{ID: "kpn", Name: "KPN", Countries: []string{"NL"}, Profiles: []string{"kpn"}},
			{ID: "tweak", Name: "Tweak", Countries: []string{"NL"}, Profiles: []string{"tweak"}},
		},
		Profiles: []config.Profile{
			{ID: "kpn", Name: "KPN", Config: value},
			{ID: "tweak", Name: "Tweak", Config: selected},
		},
	}
}

func pickNextProvider(t *testing.T, wizard *Wizard) {
	t.Helper()
	field := wizard.Form.GetFocusedField()
	assertEqual(t, "second form question", field.GetKey(), "provider")
	field.Focus()
	_, _ = field.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, _ = field.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func runProfileSwitch(t *testing.T, cancelAfter int, abort error) profileSwitchResult {
	t.Helper()
	value, selected, catalog := twoProfileCatalog(t)
	result := profileSwitchResult{before: clone(value), selected: selected}
	result.err = Configure(context.Background(), &value, catalog, func(_ context.Context, wizard *Wizard) error {
		result.calls++
		switch {
		case result.calls == 2:
			pickNextProvider(t, wizard)
		case cancelAfter > 0 && result.calls > cancelAfter:
			return abort
		}

		return nil
	})
	result.value = value

	return result
}

func TestConfigureProfileSwitch(t *testing.T) {
	t.Run("accept", func(t *testing.T) {
		result := runProfileSwitch(t, 0, nil)
		assertEqual(t, "forms", result.calls, 4)
		if result.err != nil {
			t.Fatal(result.err)
		}
		assertEqual(t, "profile", result.value.Profile, "tweak")
		assertEqual(t, "WAN interface", result.value.WAN.Interface, "eth8")
		assertEqual(t, "telemetry enabled", result.value.Telemetry.Enabled, true)
		assertEqual(t, "NAT destination", result.value.WAN.NATDestinations[0], "0.0.0.0/0")
		result.value.WAN.NATDestinations[0] = "changed"
		assertEqual(t, "profile NAT destination", result.selected.WAN.NATDestinations[0], "0.0.0.0/0")
	})

	t.Run("cancel", func(t *testing.T) {
		result := runProfileSwitch(t, 2, errProfileSwitchAborted)
		assertEqual(t, "forms", result.calls, 3)
		if !errors.Is(result.err, errProfileSwitchAborted) {
			t.Fatalf("cancel returned %v, want %v", result.err, errProfileSwitchAborted)
		}
		if !reflect.DeepEqual(result.value, result.before) {
			t.Fatal("cancel modified input")
		}
	})
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
			value := config.DefaultKPN()
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

type pageBox struct {
	lines                          []string
	top, bottom, left, right, rows int
}

func previewFrame(width, height int) *Frame {
	value := config.Default()
	fields := newFormValues(value)
	groups := configurationPages(&value, []Port{
		{Name: "eth8", Description: "connected, Internet route", Addresses: []string{"203.0.113.10/24"}, AddressesKnown: true},
		{Name: "br0", Description: "example: LAN", Addresses: []string{"192.168.1.1/24"}, AddressesKnown: true},
		{Name: "br4", Addresses: []string{"192.168.4.1/24"}, AddressesKnown: true},
	}, "IPTV DNS servers: 177.16.30.67 and 177.16.30.7.", &fields)
	frame := NewFrame(wizardForm(groups...), "Preview")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: width, Height: height})

	return frame
}

func measureBox(t *testing.T, content string, width, height int) pageBox {
	t.Helper()
	lines := strings.Split(content, "\n")
	if len(lines) != height || lipgloss.Width(content) != width {
		t.Fatalf("page is %dx%d, terminal is %dx%d", lipgloss.Width(content), len(lines), width, height)
	}
	top, bottom := boxBounds(content)
	if top < 0 || bottom < 0 {
		t.Fatal("page has no box")
	}

	return pageBox{
		lines:  lines,
		top:    top,
		bottom: bottom,
		left:   len(lines[top]) - len(strings.TrimLeft(lines[top], " ")),
		right:  width - lipgloss.Width(strings.TrimRight(lines[top], " ")),
		rows:   bottom - top,
	}
}

func assertCentered(t *testing.T, box pageBox, height int) {
	t.Helper()
	if above, below := box.top-1, height-1-box.bottom; above < 3 || below < 3 || above-below > 2 || below-above > 2 {
		t.Fatalf("box is not vertically centered: %d rows above the header, %d below", above, below)
	}
	if box.left < 20 || box.right < 20 || box.left-box.right > 2 || box.right-box.left > 2 {
		t.Fatalf("box is not horizontally centered: %d left, %d right", box.left, box.right)
	}
}

func assertBadgeRow(t *testing.T, box pageBox, badge string) {
	t.Helper()
	row := box.lines[box.top-1]
	if !strings.Contains(row, badge) || !strings.Contains(row, closeLabel) || strings.Index(row, badge) > strings.Index(row, closeLabel) {
		t.Fatalf("badge and close button missing from the row above the box: %q", row)
	}
}

func assertTitlesKeepTheirDescriptions(t *testing.T, content string) {
	t.Helper()
	for _, jammed := range []string{"quickleave?Off", "logs?Temporary", "address?Most"} {
		if strings.Contains(content, jammed) {
			t.Fatalf("confirm title ate its description: %q", jammed)
		}
	}
}

func TestConfigurationPageFits(t *testing.T) {
	const width, height = 180, 45
	frame := previewFrame(width, height)
	wantRows := 0
	for range 12 {
		if frame.wizard.Form.State == huh.StateCompleted {
			return
		}
		view := frame.View()
		if !view.AltScreen {
			t.Fatal("wizard left the alternate screen")
		}
		box := measureBox(t, view.Content, width, height)
		assertCentered(t, box, height)
		assertBadgeRow(t, box, "PREVIEW")
		assertTitlesKeepTheirDescriptions(t, view.Content)
		if wantRows == 0 {
			wantRows = box.rows
		}
		assertEqual(t, "box height", box.rows, wantRows)
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
		err := Configure(context.Background(), &value, config.DefaultCatalog(), func(_ context.Context, _ *Wizard) error {
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

func TestFrameFitsBoxToEachForm(t *testing.T) {
	settings := config.Default()
	fields := newFormValues(settings)
	groups := configurationPages(&settings, nil, "", &fields)
	frame := NewFrame(wizardForm(newPage(telemetryConsent(&settings.Telemetry))), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	short := boxRows(frame.View().Content)
	if lipgloss.Height(frame.View().Content) != 40 {
		t.Fatal("view does not fill the terminal")
	}
	frame.Update(setWizardMsg{wizard: wizardForm(groups...)})
	tall := boxRows(frame.View().Content)
	frame.Update(setWizardMsg{wizard: wizardForm(newPage(telemetryConsent(&settings.Telemetry)))})
	if tall <= short || boxRows(frame.View().Content) != short {
		t.Fatalf("box rows: short %d, tall %d, back to %d", short, tall, boxRows(frame.View().Content))
	}
}

// boxBounds returns the rows of the frame's top and bottom borders, or -1.
func boxBounds(content string) (int, int) {
	top, bottom := -1, -1
	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "╭") {
			top = i
		}
		if strings.Contains(line, "╰") {
			bottom = i
		}
	}

	return top, bottom
}

func boxRows(content string) int {
	top, bottom := boxBounds(content)

	return bottom - top
}

func boxTop(content string) int {
	top, _ := boxBounds(content)

	return max(top, 0)
}

// focusPage advances the form to the page whose first field has key.
func focusPage(frame *Frame, key string) {
	for focusedKey(frame.wizard.Form) != key {
		frame.wizard.Form.NextGroup()
	}
	frame.wizard.Form.GetFocusedField().Focus()
}

func TestEveryQuestionHasHelp(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups := configurationPages(&value, []Port{{Name: "eth8"}}, "", &fields)
	keys := []string{"country", "provider", "accept"}
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
	value := config.DefaultKPN()
	fields := newFormValues(value)
	groups := configurationPages(&value, nil, "", &fields)
	frame := NewFrame(wizardForm(groups...).steps(1, 1), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	focusPage(frame, "vlan")
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

func TestEnterOnManualNetworkEntryOpensPicker(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups, selectedPort, selectedLAN := configurationGroups(&value, []Port{{Name: "br0", AddressesKnown: true}, {Name: "eth9", AddressesKnown: true}}, "", &fields, true)
	*selectedPort = "eth8"
	frame := NewFrame(wizardForm(groups...), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	focusPage(frame, "lan")
	frame.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if frame.entry == nil {
		t.Fatal("enter on the manual row did not open the picker")
	}
	if focusedKey(frame.wizard.Form) != "lan" {
		t.Fatalf("picker changed the page to %q", focusedKey(frame.wizard.Form))
	}
	view := plain(frame)
	for _, want := range []string{"Add a network", "eth9"} {
		if !strings.Contains(view, want) {
			t.Errorf("picker lacks %q", want)
		}
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if frame.entry != nil || !slices.Contains(*selectedLAN, "eth9") || slices.Contains(*selectedLAN, manualPort) {
		t.Fatalf("picking a known interface: entry=%v selected=%v", frame.entry != nil, *selectedLAN)
	}
	if !strings.Contains(plain(frame), "[x] eth9") {
		t.Fatalf("picked interface not ticked in the list:\n%s", plain(frame))
	}
}

func TestCtrlCAsksBeforeLeaving(t *testing.T) {
	value := config.Default()
	fields := newFormValues(value)
	groups, _, _ := configurationGroups(&value, nil, "", &fields, true)
	var events []string
	frame := NewFrame(wizardForm(groups...), "")
	frame.observer = func(event Event, question string) { events = append(events, string(event)+":"+question) }
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
	groups, _, _ := configurationGroups(&value, []Port{{Name: "eth8"}, {Name: "eth9"}}, "", &fields, true)
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
	groups, _, _ := configurationGroups(&value, nil, "", &fields, true)
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

func TestFrameScalesWithTerminal(t *testing.T) {
	for _, test := range []struct{ width, want int }{{80, 72}, {120, 100}, {200, 120}, {400, 160}, {30, 22}} {
		frame := &Frame{width: test.width}
		if got := frame.contentWidth(); got != test.want {
			t.Errorf("width %d: content %d, want %d", test.width, got, test.want)
		}
	}
	value := config.Default()
	fields := newFormValues(value)
	groups, _, _ := configurationGroups(&value, nil, "", &fields, true)
	frame := NewFrame(wizardForm(groups...), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 300, Height: 80})
	if width := lipgloss.Width(strings.TrimSpace(strings.Split(frame.View().Content, "\n")[boxTop(frame.View().Content)])); width < 150 {
		t.Fatalf("box did not grow with the terminal: %d columns", width)
	}
}

func TestCountryListShowsEveryCountry(t *testing.T) {
	value := config.Default()
	calls := 0
	err := Configure(context.Background(), &value, config.DefaultCatalog(), func(_ context.Context, wizard *Wizard) error {
		calls++
		if calls > 1 {
			return huh.ErrUserAborted
		}
		frame := NewFrame(wizard, "")
		frame.Init()
		frame.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
		view := frame.View().Content
		for _, country := range config.DefaultCatalog().Countries {
			if !strings.Contains(view, country.Name) {
				t.Errorf("country %q scrolled out of view", country.Name)
			}
		}

		return nil
	})
	if !errors.Is(err, huh.ErrUserAborted) {
		t.Fatal(err)
	}
}

func TestVLANFieldAcceptsDigitsOnly(t *testing.T) {
	value := config.DefaultKPN()
	fields := newFormValues(value)
	groups := configurationPages(&value, nil, "", &fields)
	frame := NewFrame(wizardForm(groups...), "")
	frame.Init()
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	focusPage(frame, "vlan")
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

// A default-enabled setting must not authorise the first lookup on its own, so
// ConfigureFresh asks the reporting question before it calls discover, and
// hands discover the answer rather than the defaults.
func TestConfigureFreshAsksBeforeItLooksUp(t *testing.T) {
	t.Parallel()
	value := config.Default()
	if !value.Telemetry.Enabled || !value.Telemetry.NetworkIdentity {
		t.Fatal("defaults no longer enable reporting, so this asserts nothing")
	}
	var order []string
	var answered config.Telemetry
	lookups := 0
	discover := func(_ context.Context, settings config.Telemetry) (string, error) {
		lookups++
		answered = settings
		order = append(order, "lookup")

		return "kpn", nil
	}
	run := func(_ context.Context, wizard *Wizard) error {
		order = append(order, focusedKey(wizard.Form))

		return nil
	}
	if err := ConfigureFresh(t.Context(), &value, config.DefaultCatalog(), run, discover, nil); err != nil {
		t.Fatal(err)
	}
	if len(order) < 2 || order[0] != "telemetry" || order[1] != "lookup" {
		t.Fatalf("lookup did not follow the reporting question: %v", order)
	}
	if lookups != 1 {
		t.Fatalf("discover called %d times", lookups)
	}
	if answered != value.Telemetry {
		t.Fatalf("discover saw %+v, the wizard saved %+v", answered, value.Telemetry)
	}
	if slices.Contains(order[2:], "telemetry") {
		t.Fatalf("the reporting question was asked twice: %v", order)
	}
}

// A fresh wizard has already asked about reporting, so the settings pages must
// not ask again; a saved configuration is edited with the question in place.
func TestSettingsPagesAskReportingOnlyWhenNotAlreadyAnswered(t *testing.T) {
	t.Parallel()
	value := config.DefaultKPN()
	fields := newFormValues(value)
	for askConsent, want := range map[bool]bool{true: true, false: false} {
		groups, _, _ := configurationGroups(&value, nil, "", &fields, askConsent)
		found := false
		for _, group := range groups {
			if group.contains("telemetry") {
				found = true
			}
		}
		if found != want {
			t.Fatalf("askConsent=%t produced the reporting question: %t", askConsent, found)
		}
	}
}

func visibleKeys(wizard *Wizard) []string {
	var visible []string
	for _, p := range wizard.pages {
		if p.visible() {
			visible = append(visible, p.keys...)
		}
	}

	return visible
}

func runAnswered(t *testing.T, value *config.Config, answered Answered) [][]string {
	t.Helper()
	var forms [][]string
	run := func(_ context.Context, wizard *Wizard) error {
		forms = append(forms, visibleKeys(wizard))

		return nil
	}
	if err := ConfigureSuggested(t.Context(), value, config.DefaultCatalog(), run, "", answered); err != nil {
		t.Fatal(err)
	}

	return forms
}

var settingsAnswered = Answered{"profile", "wan-port", "vlan", "dhcp", "vlan-interface", "vlan-mac", "dhcp-options", "dhcp-routes", "lan", "nat", "proxy", "igmp", "quickleave", "debug"}

func TestAnsweredFieldsSkipTheirPages(t *testing.T) {
	t.Parallel()
	value := config.DefaultKPN()
	forms := runAnswered(t, &value, settingsAnswered)
	if len(forms) != 2 || !slices.Equal(forms[0], []string{"telemetry"}) || !slices.Equal(forms[1], []string{"accept"}) {
		t.Fatalf("forms asked %q", forms)
	}
	if value.Profile != "kpn" || value.WAN.VLAN != config.DefaultKPNVLAN {
		t.Fatalf("draft changed: %+v", value)
	}
}

func TestAnsweredConsentSkipsTheSettingsFormEntirely(t *testing.T) {
	t.Parallel()
	value := config.DefaultKPN()
	forms := runAnswered(t, &value, append(slices.Clone(settingsAnswered), "telemetry"))
	if len(forms) != 1 || !slices.Equal(forms[0], []string{"accept"}) {
		t.Fatalf("forms asked %q", forms)
	}
}

func TestFieldKeysCoverTheForm(t *testing.T) {
	t.Parallel()
	keys := FieldKeys()
	for _, want := range []string{"profile", "wan-port", "vlan", "dhcp", "lan", "nat", "proxy", "telemetry"} {
		if !slices.Contains(keys, want) {
			t.Errorf("FieldKeys lacks %q: %q", want, keys)
		}
	}
}
