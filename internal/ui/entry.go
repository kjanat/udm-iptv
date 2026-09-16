package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// entryPrompt is the popup a list opens for its "enter manually" row. It
// offers the interfaces the router already knows and falls back to a typed
// name. It never becomes a page of the wizard.
type entryPrompt struct {
	title, description, placeholder string
	candidates                      []huh.Option[string]
	validate                        func(string) error
	accept                          func(huh.Field, []string)
}

type entryChoice struct {
	label  string
	values []string
	typed  bool
}

const maxPopupWidth = 72

var (
	entryErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ED567A"))
	entryChoiceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	entryCursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F780E2"))
	entryTypedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#02BF87"))
)

func entryText(text string) bool {
	for _, r := range text {
		if r < ' ' || r == 0x7f {
			return false
		}
	}

	return text != ""
}

// choices puts the typed text first, as a free entry, so Enter takes what
// the user wrote; the known interfaces matching the text follow. A typed
// text that names a known interface exactly offers only that interface.
func (entry *entryPrompt) choices(text string) []entryChoice {
	needle := strings.ToLower(strings.TrimSpace(text))
	var result []entryChoice
	names := splitList(text)
	exact := len(names) == 1 && hasOption(entry.candidates, names[0])
	if len(names) > 0 && !exact {
		result = append(result, entryChoice{label: "Use " + strings.Join(names, ", "), values: names, typed: true})
	}
	for _, candidate := range entry.candidates {
		if needle == "" || strings.Contains(strings.ToLower(candidate.Key), needle) {
			result = append(result, entryChoice{label: candidate.Key, values: []string{candidate.Value}})
		}
	}

	return result
}

func (frame *Frame) openEntry(entry *entryPrompt) {
	frame.entry, frame.entryText, frame.entryCursor, frame.entryErr = entry, "", 0, nil
	frame.observe("entry.open")
}

func (frame *Frame) closeEntry() {
	frame.entry, frame.entryText, frame.entryCursor, frame.entryErr = nil, "", 0, nil
}

// answerEntry handles a key while the entry popup is open. Accepting a
// choice mutates the list, so the form gets a refresh message to redraw.
func (frame *Frame) answerEntry(text string, enter, backspace, escape, up, down bool) tea.Cmd {
	choices := frame.entry.choices(frame.entryText)
	switch {
	case escape:
		frame.closeEntry()
	case up && len(choices) > 0:
		frame.entryCursor = (frame.entryCursor + len(choices) - 1) % len(choices)
	case down && len(choices) > 0:
		frame.entryCursor = (frame.entryCursor + 1) % len(choices)
	case enter:
		if len(choices) == 0 {
			return nil
		}
		choice := choices[min(frame.entryCursor, len(choices)-1)]
		if choice.typed {
			for _, name := range choice.values {
				if err := frame.entry.validate(name); err != nil {
					frame.entryErr = err

					return nil
				}
			}
		}
		frame.entry.accept(frame.wizard.Form.GetFocusedField(), choice.values)
		frame.closeEntry()

		return frame.forward(searchChangedMsg{})
	case backspace:
		runes := []rune(frame.entryText)
		if len(runes) > 0 {
			frame.entryText = string(runes[:len(runes)-1])
		}
		frame.entryCursor, frame.entryErr = 0, nil
	case entryText(text):
		frame.entryText += text
		frame.entryCursor, frame.entryErr = 0, nil
	}

	return nil
}

func (frame *Frame) entryPopup() string {
	width := min(max(frame.contentWidth()-popupPadding, minContentWidth), maxPopupWidth)
	body := lipgloss.NewStyle().Width(width).Render(frame.entry.description)
	line := frame.entryText
	if line == "" {
		line = progressTextStyle.Render(frame.entry.placeholder)
	}
	rows := []string{helpTitleStyle.Render(frame.entry.title), "", body, "", helpKeyStyle.Render("> ") + line}
	choices := frame.entry.choices(frame.entryText)
	cursor := min(frame.entryCursor, max(len(choices)-1, 0))
	for i, choice := range choices {
		prefix, style := "  ", entryChoiceStyle
		if choice.typed {
			style = entryTypedStyle
		}
		if i == cursor {
			prefix = entryCursorStyle.Render("> ")
		}
		rows = append(rows, prefix+style.Render(choice.label))
	}
	if len(choices) == 0 {
		rows = append(rows, progressTextStyle.Render("  type a name"))
	}
	if frame.entryErr != nil {
		rows = append(rows, entryErrorStyle.Render(frame.entryErr.Error()))
	}
	rows = append(rows, "", progressTextStyle.Render("↑/↓ choose  enter add  esc cancel"))

	return popupStyle.Width(width + popupPadding).Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}
