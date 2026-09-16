package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/kjanat/udm-iptv/internal/config"
)

func TestCascadeStepsBackAndForward(t *testing.T) {
	country, choice := "NL", choiceOf("kpn", "kpn")
	answers := []*string{&country, &choice}
	var keys []string
	calls := 0
	run := func(_ context.Context, wizard *Wizard) error {
		calls++
		keys = append(keys, focusedKey(wizard.Form))
		if calls == 2 {
			return ErrBack
		}

		return nil
	}
	asked, err := cascade(context.Background(), run, profileSteps(config.DefaultCatalog(), ""), answers, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"country", "provider", "country", "provider"}
	if len(keys) != len(want) {
		t.Fatalf("forms = %v", keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("forms = %v, want %v", keys, want)
		}
	}
	if asked != 2 || choice != choiceOf("kpn", "kpn") {
		t.Fatalf("asked %d, choice %q", asked, choice)
	}
}

func TestCascadeBackOutOfFirstStepPropagates(t *testing.T) {
	country := "NL"
	run := func(context.Context, *Wizard) error { return ErrBack }
	_, err := cascade(context.Background(), run, profileSteps(config.DefaultCatalog(), "")[:1], []*string{&country}, 0, 0)
	if err == nil || err.Error() != ErrBack.Error() {
		t.Fatalf("err = %v", err)
	}
}

func TestShiftTabOnFirstPageStepsBackOnlyWithHistory(t *testing.T) {
	for _, before := range []int{0, 1} {
		value := config.Default()
		frame := NewFrame(wizardForm(newPage(telemetryConsent(&value.Telemetry))).steps(before, 0), "")
		frame.Init()
		frame.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		_, cmd := frame.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		wentBack := frame.wizard.wentBack
		if wentBack != (before > 0) {
			t.Fatalf("before=%d: wentBack=%v", before, wentBack)
		}
		if before > 0 {
			if _, ok := cmd().(wizardDoneMsg); !ok {
				t.Fatal("stepping back did not finish the form")
			}
			if frame.wizard.Form.State == huh.StateAborted {
				t.Fatal("stepping back aborted the form")
			}
		}
	}
}
