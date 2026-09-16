package ui

import (
	"strings"
	"sync"
	"unicode"

	"charm.land/huh/v2"
)

// searchable is a select whose options narrow as the user types. Pinned
// options stay visible whatever the filter says. huh evaluates the option
// and title functions on their own goroutines, so filter reads take the lock.
type searchable struct {
	mu     sync.Mutex
	filter string
	value  *string
	all    []huh.Option[string]
	pinned []huh.Option[string]
}

func (s *searchable) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.filter
}

func (s *searchable) options() []huh.Option[string] {
	return s.matching(s.text())
}

func (s *searchable) matching(filter string) []huh.Option[string] {
	needle := strings.ToLower(strings.TrimSpace(filter))
	result := make([]huh.Option[string], 0, len(s.all)+len(s.pinned))
	for _, option := range s.all {
		if needle == "" || strings.Contains(strings.ToLower(option.Key), needle) {
			result = append(result, option)
		}
	}

	return append(result, s.pinned...)
}

func (s *searchable) title(text string) func() string {
	return func() string {
		filter := s.text()
		if filter == "" {
			return text
		}

		return text + "  🔍 " + filter
	}
}

// keystroke applies a typed character, backspace or escape to the filter and
// reports whether the key was consumed.
func (s *searchable) keystroke(text string, backspace, escape bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case escape && s.filter != "":
		s.filter = ""
	case backspace && s.filter != "":
		runes := []rune(s.filter)
		s.filter = string(runes[:len(runes)-1])
	case text != "" && searchText(text):
		s.filter += text
	default:
		return false
	}
	if s.filter != "" && s.value != nil {
		*s.value = s.matching(s.filter)[0].Value
	}

	return true
}

func searchText(text string) bool {
	for _, r := range text {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' ' && r != '-' {
			return false
		}
	}

	return true
}

// allChoices is the value of the automatic "All …" row. A list whose answer
// filters the next list always ends with one, so the user can skip the filter.
const allChoices = "*"

// searchSelect builds a type-to-filter select. A non-empty filters names, in
// the plural, what the answer narrows down, and pins "All <filters>" before
// any other pinned rows.
func searchSelect(key, title, description, filters string, s *searchable, value *string) *huh.Select[string] {
	s.value = value
	if filters != "" {
		s.pinned = append([]huh.Option[string]{huh.NewOption("All "+filters, allChoices)}, s.pinned...)
	}

	return huh.NewSelect[string]().Key(key).
		TitleFunc(s.title(title), &s.filter).
		Description(description).
		Options(s.options()...).
		OptionsFunc(s.options, &s.filter).
		Height(len(s.all) + len(s.pinned) + selectChrome).
		Value(value)
}

// searchChangedMsg nudges the form so bound option lists re-evaluate.
type searchChangedMsg struct{}
