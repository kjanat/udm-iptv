package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/kjanat/udm-iptv/internal/config"
)

type suggestionResult struct {
	value, before config.Config
	err           error
	calls         int
}

func assertPreselected(t *testing.T, wizard *Wizard, preselected map[int]string, call int) {
	t.Helper()
	want, ok := preselected[call]
	if !ok {
		return
	}
	if got := wizard.Form.GetFocusedField().GetValue(); got != want {
		t.Fatalf("form %d preselects %v, want %q", call, got, want)
	}
}

func runSuggestedWizard(t *testing.T, suggestion string, preselected map[int]string, abortAt int) suggestionResult {
	t.Helper()
	value := config.Default()
	result := suggestionResult{before: clone(value)}
	result.err = ConfigureSuggested(context.Background(), &value, config.DefaultCatalog(), func(_ context.Context, wizard *Wizard) error {
		result.calls++
		assertPreselected(t, wizard, preselected, result.calls)
		if result.calls == abortAt {
			return huh.ErrUserAborted
		}

		return nil
	}, suggestion, nil)
	result.value = value

	return result
}

func TestSuggestedProviderIsDraftUntilReview(t *testing.T) {
	const reviewForm = 4
	preselected := map[int]string{1: "NL", 2: choiceOf("tweak", "tweak")}

	t.Run("accept", func(t *testing.T) {
		result := runSuggestedWizard(t, "tweak", preselected, 0)
		assertEqual(t, "forms", result.calls, reviewForm)
		if result.err != nil {
			t.Fatalf("selection not applied: %v", result.err)
		}
		assertEqual(t, "profile", result.value.Profile, "tweak")
		if reflect.DeepEqual(result.value, result.before) {
			t.Fatal("accepted review left the settings untouched")
		}
	})

	t.Run("decline", func(t *testing.T) {
		result := runSuggestedWizard(t, "tweak", preselected, reviewForm)
		assertEqual(t, "forms", result.calls, reviewForm)
		if !errors.Is(result.err, huh.ErrUserAborted) {
			t.Fatalf("decline returned %v, want %v", result.err, huh.ErrUserAborted)
		}
		if !reflect.DeepEqual(result.value, result.before) {
			t.Fatal("cancelled review changed settings")
		}
	})
}

func TestReviewDeclinePreservesConfiguration(t *testing.T) {
	value := config.DefaultKPN()
	original := clone(value)
	calls := 0
	err := Configure(context.Background(), &value, config.DefaultCatalog(), func(_ context.Context, wizard *Wizard) error {
		form := wizard.Form
		calls++
		if calls == 4 {
			field := form.GetFocusedField()
			field.Focus()
			_, _ = field.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
		}

		return nil
	})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(value, original) {
		t.Fatalf("decline: %v", err)
	}
}

func TestProviderSuggestionCanBeOverridden(t *testing.T) {
	value := config.Default()
	chosen := ""
	calls := 0
	catalog := config.DefaultCatalog()
	err := ConfigureSuggested(t.Context(), &value, catalog, func(_ context.Context, wizard *Wizard) error {
		form := wizard.Form
		calls++
		if calls == 2 {
			field := form.GetFocusedField()
			field.Focus()
			_, _ = field.Update(tea.KeyPressMsg{Code: tea.KeyUp})
			value, ok := field.GetValue().(string)
			if !ok {
				t.Fatalf("GetValue() returned %T, want string", field.GetValue())
			}
			chosen = value
		}

		return nil
	}, "tweak", nil)
	provider, profile, _ := strings.Cut(chosen, "/")
	if err != nil || provider == "tweak" || value.Profile != profile || catalog.ProfilesOf(provider)[0].ID != profile {
		t.Fatalf("manual selection lost: %s, %v", chosen, err)
	}
}

func TestInlineInputValidation(t *testing.T) {
	for _, value := range []string{"213.75.0.0/16", "", "0.0.0.0/0 148.122.7.125/32", "213.75.0.0/16, 217.166.0.0/16", "213.75.0.0/16;217.166.0.0/16"} {
		err := validatePrefixes(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"broken", "1.2.3.4", "::/0"} {
		if validatePrefixes(value) == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if got := splitList(" 1.0.0.0/8,2.0.0.0/8 ;\t3.0.0.0/8, "); !reflect.DeepEqual(got, []string{"1.0.0.0/8", "2.0.0.0/8", "3.0.0.0/8"}) {
		t.Fatalf("splitList = %v", got)
	}
	if config.ValidateInterfaceName("eth8") != nil || config.ValidateInterfaceName("../bad") == nil {
		t.Fatal("bad interface validation")
	}
}
