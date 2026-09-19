package ui

import (
	"io"
	"os"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
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

var (
	labelledLine = regexp.MustCompile(`^([A-Za-z][^:\n]*): (.*)$`)
	helpCommand  = regexp.MustCompile(`^(  )([a-z][a-z0-9-]*)( {2,}.*)$`)
	helpFlag     = regexp.MustCompile(`(^|[ ,])(-[a-zA-Z]|--[a-z][a-z0-9-]*)`)
	goodTokens   = regexp.MustCompile(`\b(active/running|applied|routed|enabled|up|managed|yes|healthy|completed)\b`)
	warnTokens   = regexp.MustCompile(`\b(unmanaged|unavailable|not checked|none recorded|none configured|no route via \S+|disabled)\b`)
	badTokens    = regexp.MustCompile(`\b(not applied|failed|inactive|recorded by dpkg while|unhealthy|timed out)\b`)
)

// StatusText colours the text form of a status report or a capture for a
// terminal: labels bold, section headings accented, and the words that say
// whether something works in green, yellow or red.
func StatusText(text string) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = statusLine(line, index == 0)
	}

	return strings.Join(lines, "\n")
}

func statusLine(line string, first bool) string {
	switch {
	case first && strings.HasPrefix(line, "udm-iptv "):
		return headingStyle.Render(line)
	case line == "":
		return line
	case !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":"):
		return headingStyle.Render(line)
	case !strings.HasPrefix(line, " ") && !strings.Contains(line, ": "):
		return headingStyle.Render(line)
	}
	if match := labelledLine.FindStringSubmatch(line); match != nil && !strings.HasPrefix(line, " ") {
		return labelStyle.Render(match[1]) + ": " + verdictTokens(match[2])
	}

	return verdictTokens(line)
}

func verdictTokens(text string) string {
	text = badTokens.ReplaceAllStringFunc(text, render(badStyle))
	text = warnTokens.ReplaceAllStringFunc(text, render(warnStyle))

	return goodTokens.ReplaceAllStringFunc(text, render(goodStyle))
}

func render(style lipgloss.Style) func(string) string {
	return func(text string) string { return style.Render(text) }
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
