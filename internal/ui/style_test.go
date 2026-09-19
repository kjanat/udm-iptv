package ui

import (
	"bytes"
	"strings"
	"testing"
)

const escape = "\x1b["

func TestStatusTextColoursVerdictsAndLabels(t *testing.T) {
	t.Parallel()
	text := strings.Join([]string{
		"udm-iptv 5.0.0-preview.3",
		"Installation: package 5.0.0-preview.1 recorded by dpkg while 5.0.0-preview.3 runs; udm-iptv upgrade reinstalls the package",
		"Service: active/running (enabled, restarts: 0)",
		"NAT evidence per destination:",
		"  213.75.0.0/16: routed (213.75.112.0/21 via 10.207.64.1), 5 packets, 560 B",
		"  217.166.0.0/16: no route via iptv, 0 packets, 0 B",
		"",
		"Downstream checks",
		"br0: link=up, snooping=enabled, querier=disabled",
	}, "\n")
	styled := StatusText(text)
	for _, want := range []string{
		headingStyle.Render("udm-iptv 5.0.0-preview.3"),
		labelStyle.Render("Installation") + ": ",
		badStyle.Render("recorded by dpkg while"),
		goodStyle.Render("active/running"),
		headingStyle.Render("NAT evidence per destination:"),
		goodStyle.Render("routed"),
		warnStyle.Render("no route via iptv"),
		headingStyle.Render("Downstream checks"),
		warnStyle.Render("disabled"),
	} {
		if !strings.Contains(styled, want) {
			t.Errorf("missing %q in:\n%s", want, styled)
		}
	}
	if strings.Count(styled, "\n") != strings.Count(text, "\n") {
		t.Fatal("styling changed the line count")
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
	if _, err := writer.Write([]byte(StatusText("Service: active/running\n"))); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != "Service: active/running\n" || strings.Contains(got, escape) {
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
