package ui

import (
	"io"
	"os"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/kjanat/udm-iptv/internal/diagnostics"
)

var (
	headingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	labelStyle   = lipgloss.NewStyle().Bold(true)
	nameStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	flagStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	goodStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	badStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
)

// Styled returns a writer that keeps the colour the terminal behind out can
// show and strips it from a pipe or a file.
func Styled(out io.Writer) io.Writer {
	return colorprofile.NewWriter(out, os.Environ())
}

// Terminal returns the stream behind a styled writer, for a program that
// drives the terminal itself.
func Terminal(out io.Writer) io.Writer {
	if styled, ok := out.(*colorprofile.Writer); ok {
		return styled.Forward
	}

	return out
}

var (
	helpCommand = regexp.MustCompile(`^(  )([a-z][a-z0-9-]*)( {2,}.*)$`)
	helpFlag    = regexp.MustCompile(`(^|[ ,])(-[a-zA-Z]|--[a-z][a-z0-9-]*)`)
)

// Status renders semantic report fragments without inspecting their text.
func Status(value diagnostics.Snapshot) string {
	return diagnostics.RenderSnapshotStyled(value, reportText)
}

func reportText(role diagnostics.ReportRole, text string) string {
	switch role {
	case diagnostics.ReportPlain:
		return text
	case diagnostics.ReportHeading:
		return headingStyle.Render(text)
	case diagnostics.ReportLabel:
		return labelStyle.Render(text)
	case diagnostics.ReportGood:
		return goodStyle.Render(text)
	case diagnostics.ReportWarning:
		return warnStyle.Render(text)
	case diagnostics.ReportBad:
		return badStyle.Render(text)
	default:
		return text
	}
}

// HelpText colours Cobra's help output: section headings, command names in
// the command lists, and flags.
func HelpText(text string) string {
	lines := strings.Split(text, "\n")
	inCommands := false
	for index, line := range lines {
		switch {
		case line == "":
			inCommands = false
		case !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":"):
			inCommands = strings.HasSuffix(line, "Commands:")
			lines[index] = headingStyle.Render(line)
		case inCommands:
			lines[index] = helpCommand.ReplaceAllStringFunc(line, func(entry string) string {
				match := helpCommand.FindStringSubmatch(entry)

				return match[1] + nameStyle.Render(match[2]) + match[3]
			})
		default:
			lines[index] = helpFlag.ReplaceAllStringFunc(line, func(flag string) string {
				match := helpFlag.FindStringSubmatch(flag)

				return match[1] + flagStyle.Render(match[2])
			})
		}
	}

	return strings.Join(lines, "\n")
}

// ErrorText colours an error line for the terminal.
func ErrorText(text string) string {
	return badStyle.Render(text)
}
