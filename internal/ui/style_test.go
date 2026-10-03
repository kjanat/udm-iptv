package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/kjanat/udm-iptv/internal/diagnostics"
	"github.com/kjanat/udm-iptv/internal/service"
)

const escape = "\x1b["

func TestStatusColorsStructuredOutcomeWithoutRecoloringEvidence(t *testing.T) {
	t.Parallel()
	const evidence = "failed enabled not applied active/running"
	value := diagnostics.Snapshot{Version: "test", ProxyConfig: new(evidence), Lease: &service.LeaseState{Applied: false, Failure: evidence}}
	styled := Status(value)
	for _, want := range []string{headingStyle.Render("udm-iptv test"), labelStyle.Render("Profile"), badStyle.Render("not applied") + ": " + evidence, "Generated proxy configuration:\n" + evidence} {
		if !strings.Contains(styled, want) {
			t.Errorf("missing %q in:\n%s", want, styled)
		}
	}
	if strings.Contains(styled, goodStyle.Render("applied")) {
		t.Fatal("failed lease recolored as success")
	}
	if ansi.Strip(styled) != diagnostics.RenderSnapshot(value) {
		t.Fatal("terminal styling changed diagnostic evidence")
	}
}

func TestHelpTextColoursSectionsCommandsAndFlags(t *testing.T) {
	t.Parallel()
	text := strings.Join([]string{
		"Usage:",
		"  udm-iptv [command]",
		"",
		"Management Commands:",
		"  configure       Configure IPTV interactively",
		"",
		"Flags:",
		"      --config string   configuration file",
		"  -h, --help            help for udm-iptv",
	}, "\n")
	styled := HelpText(text)
	for _, want := range []string{
		headingStyle.Render("Usage:"),
		headingStyle.Render("Management Commands:"),
		"  " + nameStyle.Render("configure") + "       Configure IPTV interactively",
		flagStyle.Render("--config"),
		flagStyle.Render("-h") + ", " + flagStyle.Render("--help"),
	} {
		if !strings.Contains(styled, want) {
			t.Errorf("missing %q in:\n%s", want, styled)
		}
	}
	if strings.Contains(styled, nameStyle.Render("udm-iptv")) {
		t.Fatal("the usage line was styled as a command entry")
	}
}

// Piped output carries no escape codes.
func TestStyledWriterStripsColourWithoutATerminal(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	writer := Styled(&buffer)
	if _, err := writer.Write([]byte(Status(diagnostics.Snapshot{Version: "test"}))); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != diagnostics.RenderSnapshot(diagnostics.Snapshot{Version: "test"}) || strings.Contains(got, escape) {
		t.Fatalf("piped output = %q", got)
	}
}

// bubbletea drives the terminal file itself; a styling wrapper in between
// leaves it drawing nothing.
func TestTerminalUnwrapsTheStyledWriter(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	if got := Terminal(Styled(&buffer)); got != &buffer {
		t.Fatalf("Terminal returned %T", got)
	}
	if got := Terminal(&buffer); got != &buffer {
		t.Fatalf("a plain writer came back as %T", got)
	}
}
