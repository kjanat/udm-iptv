package ui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/kjanat/udm-iptv/internal/config"
)

func TestImprovementPromptCopy(t *testing.T) {
	settings := config.Default().Telemetry
	original := settings
	form := wizardForm(newPage(telemetryConsent(&settings))).Form
	form.Init()
	form.Update(tea.WindowSizeMsg{Width: 180, Height: 45})
	view := form.View()
	if lipgloss.Width(view) > preferredContentWidth {
		t.Fatalf("form stretched to %d columns", lipgloss.Width(view))
	}
	if !strings.Contains(view, "Help improve udm-iptv?") {
		t.Fatal("missing improvement prompt")
	}
	for _, text := range []string{"Sentry", "IP/PTR", "installation ID", "Optional reporting", "What may be sent?", "Errors", "Network identity"} {
		if strings.Contains(view, text) {
			t.Fatalf("unexpected prompt text %q", text)
		}
	}
	if focusedKey(form) != "telemetry" {
		t.Fatal("prompt is not the telemetry confirm")
	}
	form.NextGroup()
	if form.State != huh.StateCompleted {
		t.Fatal("prompt opened a follow-up page")
	}
	if !reflect.DeepEqual(settings, original) {
		t.Fatal("prompt changed product selection")
	}
}

// The wizard is the consent point, so its help must match docs/telemetry.md
// rather than promising anonymity the reports do not provide.
func TestTelemetryHelpDisclosesIdentifyingData(t *testing.T) {
	t.Parallel()
	help, found := fieldHelp["telemetry"]
	if !found {
		t.Fatal("the telemetry question has no help")
	}
	for _, required := range []string{"not anonymous", "installation ID", "public IP", "reverse-DNS", "Sentry"} {
		if !strings.Contains(help.text, required) {
			t.Errorf("telemetry help omits %q", required)
		}
	}
	for _, forbidden := range []string{"anonymous error reports", "Nothing about your network addresses"} {
		if strings.Contains(help.text, forbidden) {
			t.Errorf("telemetry help claims %q", forbidden)
		}
	}
}
