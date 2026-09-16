package ui

import (
	"context"
	"errors"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/kjanat/udm-iptv/internal/config"
)

func TestSuggestedProviderIsDraftUntilReview(t *testing.T) {
	for _, accept := range []bool{false, true} {
		value := config.Default()
		original := clone(value)
		calls := 0
		err := ConfigureSuggested(context.Background(), &value, config.Profiles(), func(_ context.Context, form *huh.Form) error {
			calls++
			if calls == 1 && form.GetFocusedField().GetValue() != "tweak" {
				t.Fatal("suggestion not preselected")
			}
			if calls == 3 && !accept {
				return huh.ErrUserAborted
			}

			return nil
		}, "tweak")
		if calls != 3 {
			t.Fatalf("missing review: %d calls", calls)
		}
		if accept {
			if err != nil || value.Profile != "tweak" {
				t.Fatalf("selection not applied: %v", err)
			}
		} else if !errors.Is(err, huh.ErrUserAborted) || !reflect.DeepEqual(value, original) {
			t.Fatal("cancelled review changed settings")
		}
	}
}

func TestReviewDeclinePreservesConfiguration(t *testing.T) {
	value := config.Default()
	original := clone(value)
	calls := 0
	err := Configure(context.Background(), &value, config.Profiles(), func(_ context.Context, form *huh.Form) error {
		calls++
		if calls == 3 {
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
	err := ConfigureSuggested(t.Context(), &value, config.Profiles(), func(_ context.Context, form *huh.Form) error {
		calls++
		if calls == 1 {
			field := form.GetFocusedField()
			field.Focus()
			_, _ = field.Update(tea.KeyPressMsg{Code: tea.KeyUp})
			chosen = field.GetValue().(string)
		}

		return nil
	}, "tweak")
	if err != nil || chosen == "tweak" || value.Profile != chosen {
		t.Fatalf("manual selection lost: %s, %v", chosen, err)
	}
}

func TestInlineInputValidation(t *testing.T) {
	for _, value := range []string{"213.75.0.0/16", "", "0.0.0.0/0 148.122.7.125/32"} {
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
	if validateInterface("eth8") != nil || validateInterface("../bad") == nil {
		t.Fatal("bad interface validation")
	}
}
