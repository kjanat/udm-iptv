package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/kjanat/udm-iptv/internal/config"
)

var ansiSequence = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plain(frame *Frame) string {
	return ansiSequence.ReplaceAllString(frame.View().Content, "")
}

// harness runs a Frame inside teatest and answers probe messages on the
// program goroutine, so tests read state without racing the renderer.
type harness struct{ frame *Frame }

type probe struct {
	inspect func(*Frame)
	done    chan struct{}
}

func (h harness) Init() tea.Cmd { return h.frame.Init() }

func (h harness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if p, ok := msg.(probe); ok {
		p.inspect(h.frame)
		close(p.done)

		return h, nil
	}
	_, cmd := h.frame.Update(msg)

	return h, cmd
}

func (h harness) View() tea.View { return h.frame.View() }

// run starts the frame in a real bubbletea program on a fake terminal.
func run(t *testing.T, frame *Frame) *teatest.TestModel {
	t.Helper()

	return teatest.NewTestModel(t, harness{frame: frame}, teatest.WithInitialTermSize(120, 40))
}

// inspect runs fn on the program goroutine after every message sent so far.
func inspect(tm *teatest.TestModel, fn func(*Frame)) {
	done := make(chan struct{})
	tm.Send(probe{inspect: fn, done: done})
	<-done
}

const (
	settleTimeout  = 2 * time.Second
	settleInterval = 20 * time.Millisecond
)

// until re-inspects the frame until cond holds, giving huh's asynchronous
// commands time to land.
func until(t *testing.T, tm *teatest.TestModel, cond func(*Frame) bool) {
	t.Helper()
	deadline := time.Now().Add(settleTimeout)
	for {
		ok := false
		inspect(tm, func(frame *Frame) { ok = cond(frame) })
		if ok {
			return
		}
		if time.Now().After(deadline) {
			var view string
			inspect(tm, func(frame *Frame) { view = plain(frame) })
			t.Fatalf("condition never held:\n%s", view)
		}
		time.Sleep(settleInterval)
	}
}

func press(tm *teatest.TestModel, codes ...rune) {
	for _, code := range codes {
		tm.Send(tea.KeyPressMsg{Code: code})
	}
}

// shown waits until the frame renders text.
func shown(t *testing.T, tm *teatest.TestModel, text string) {
	t.Helper()
	until(t, tm, func(frame *Frame) bool { return strings.Contains(plain(frame), text) })
}

// finish stops the program and returns the frame as it ended.
func finish(t *testing.T, tm *teatest.TestModel) *Frame {
	t.Helper()
	var frame *Frame
	inspect(tm, func(f *Frame) { frame = f })
	_ = tm.Quit()
	tm.WaitFinished(t)

	return frame
}

// stepFrame builds the frame for one step of the profile chain.
func stepFrame(t *testing.T, index int, previous string, chosen *string) *Frame {
	t.Helper()
	steps := profileSteps(config.DefaultCatalog(), "")
	p, ok := steps[index].page(previous, index == len(steps)-1, chosen)
	if !ok {
		t.Fatalf("step %d has no options after %q", index, previous)
	}

	return NewFrame(wizardForm(p), "")
}

func countryFrame(t *testing.T) (*Frame, *string) {
	t.Helper()
	chosen := "NL"

	return stepFrame(t, 0, "", &chosen), &chosen
}

func TestTypingFiltersCountriesAndKeepsPinnedRows(t *testing.T) {
	frame, chosen := countryFrame(t)
	tm := run(t, frame)
	tm.Type("sw")
	shown(t, tm, "🔍 sw")
	view := plain(finish(t, tm))
	for _, want := range []string{"🔍 sw", "Switzerland", "All countries", "Manual"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q", want)
		}
	}
	for _, unwanted := range []string{"Netherlands", "Brazil", "/ filter"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("view still shows %q", unwanted)
		}
	}
	if *chosen != "CH" {
		t.Fatalf("cursor did not move to the first match: %q", *chosen)
	}
}

func TestBackspaceAndEscapeEditTheSearch(t *testing.T) {
	frame, _ := countryFrame(t)
	tm := run(t, frame)
	tm.Type("ger")
	press(tm, tea.KeyBackspace)
	shown(t, tm, "🔍 ge")
	press(tm, tea.KeyEscape)
	shown(t, tm, "Netherlands")
	press(tm, tea.KeyEscape)
	shown(t, tm, "Leave the wizard?")
	final := finish(t, tm)
	if !final.quitPrompt {
		t.Fatal("escape on an empty search must offer to leave")
	}
}

func TestSlashDoesNotOpenHuhFilter(t *testing.T) {
	frame, _ := countryFrame(t)
	tm := run(t, frame)
	tm.Type("/")
	shown(t, tm, "type")
	final := finish(t, tm)
	if filtering(final.wizard.Form) {
		t.Fatal("slash opened the built-in filter")
	}
	if !strings.Contains(plain(final), "type search") {
		t.Fatal("help hint does not say type search")
	}
}

func TestAllCountriesListsEveryProviderWithCountry(t *testing.T) {
	chosen := choiceOf("kpn", "kpn")
	tm := run(t, stepFrame(t, 1, allChoices, &chosen))
	shown(t, tm, "Vivo (GVT network, Brazil)")
	view := plain(finish(t, tm))
	for _, want := range []string{"> KPN (Netherlands)", "Vivo (São Paulo network, Brazil)", "Vivo (GVT network, Brazil)"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q", want)
		}
	}
}

func TestTypedNetworksAppearTickedInTheList(t *testing.T) {
	pages, selected := lanGroups([]string{"br0"}, []Port{{Name: "br0", Addresses: []string{"192.168.1.1/24"}, AddressesKnown: true}})
	tm := run(t, NewFrame(wizardForm(pages...), ""))
	shown(t, tm, "Enter another interface manually")
	press(tm, tea.KeyDown)
	tm.Send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	shown(t, tm, "Add a network")
	tm.Type("br4, br5")
	shown(t, tm, "Use br4, br5")
	press(tm, tea.KeyEnter)
	shown(t, tm, "br5 (VLAN 5, entered manually)")
	press(tm, tea.KeyEnd, tea.KeyEnter)
	shown(t, tm, "Add a network")
	tm.Type("../bad")
	press(tm, tea.KeyEnter)
	shown(t, tm, "interface name")
	press(tm, tea.KeyEscape)
	final := finish(t, tm)
	if final.entry != nil || final.quitPrompt {
		t.Fatal("escape must only close the picker")
	}
	view := plain(final)
	for _, want := range []string{"[x] br0", "[x] br4 (VLAN 4, entered manually)", "[x] br5 (VLAN 5, entered manually)", "[ ] Enter another interface manually…"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if got := resolveLAN(*selected); len(got) != 3 {
		t.Fatalf("resolved networks = %v", got)
	}
}
