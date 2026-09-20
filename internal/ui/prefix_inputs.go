package ui

import (
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// prefixInputs is one wizard field containing separately editable networks.
// Keeping one field key preserves answer skipping, help and review behavior.
type prefixInputs struct {
	*huh.Input

	value  *string
	rows   []*huh.Input
	cursor int
	err    error
	theme  huh.Theme
	keymap *huh.KeyMap
	width  int
}

const prefixInputChrome = 4

func newPrefixInputs(value *string) *prefixInputs {
	p := &prefixInputs{Input: huh.NewInput().Key("nat"), value: value, width: preferredContentWidth}
	for _, prefix := range splitList(*value) {
		p.appendRow(prefix)
	}
	if len(p.rows) == 0 {
		p.appendRow("")
	}
	return p
}

func (p *prefixInputs) appendRow(value string) {
	row := huh.NewInput().Value(&value).Placeholder("213.75.0.0/16").Validate(validateOptionalPrefix)
	if p.theme != nil {
		row.WithTheme(p.theme)
	}
	if p.keymap != nil {
		row.WithKeyMap(p.keymap)
	}
	row.WithWidth(max(p.width-prefixInputChrome, minContentWidth))
	p.rows = append(p.rows, row)
}

func (p *prefixInputs) syncValue() {
	var values []string
	for _, row := range p.rows {
		value := prefixValue(row)
		if value != "" {
			values = append(values, value)
		}
	}
	*p.value = strings.Join(values, " ")
}

func (p *prefixInputs) Init() tea.Cmd  { return p.rows[p.cursor].Init() }
func (p *prefixInputs) Focus() tea.Cmd { return p.rows[p.cursor].Focus() }
func (p *prefixInputs) Blur() tea.Cmd  { return p.rows[p.cursor].Blur() }
func (p *prefixInputs) Error() error   { return p.err }
func (p *prefixInputs) GetValue() any  { return *p.value }

func (p *prefixInputs) selectRow(index int) tea.Cmd {
	p.rows[p.cursor].Blur()
	p.cursor = index
	p.err = nil
	return p.rows[p.cursor].Focus()
}

func (p *prefixInputs) validateRows() bool {
	for i, row := range p.rows {
		if err := validateOptionalPrefix(prefixValue(row)); err != nil {
			p.selectRow(i)
			p.err = fmt.Errorf("network %d: %w", i+1, err)
			return false
		}
	}
	p.err = nil
	return true
}

func (p *prefixInputs) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	if press, ok := msg.(tea.KeyPressMsg); ok {
		p.err = nil
		if handled, cmd := p.handleKey(press); handled {
			return p, cmd
		}
	}
	_, cmd := p.rows[p.cursor].Update(msg)
	p.syncValue()
	return p, cmd
}

func (p *prefixInputs) handleKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "ctrl+n":
		p.appendRow("")
		return true, p.selectRow(len(p.rows) - 1)
	case "ctrl+d":
		p.removeRow()
		return true, p.rows[p.cursor].Focus()
	case "up", "shift+tab":
		if p.cursor > 0 {
			return true, p.selectRow(p.cursor - 1)
		}
		return true, huh.PrevField
	case "down", "tab":
		if p.cursor+1 < len(p.rows) {
			return true, p.selectRow(p.cursor + 1)
		}
		return true, nil
	case "enter":
		if p.validateRows() {
			p.syncValue()
			return true, huh.NextField
		}
		return true, nil
	default:
		return false, nil
	}
}

func (p *prefixInputs) removeRow() {
	p.rows[p.cursor].Blur()
	p.rows = append(p.rows[:p.cursor], p.rows[p.cursor+1:]...)
	if len(p.rows) == 0 {
		p.appendRow("")
	}
	p.cursor = min(p.cursor, len(p.rows)-1)
	p.err = nil
	p.syncValue()
}

func (p *prefixInputs) View() string {
	lines := []string{accentStyle.Render("IPTV service networks"), "Keep your provider's defaults unless instructed otherwise."}
	const visibleRows = 4
	start := max(0, p.cursor-visibleRows+1)
	end := min(len(p.rows), start+visibleRows)
	for _, row := range p.rows[start:end] {
		lines = append(lines, lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Width(max(p.width-2, 1)).Render(row.View()))
	}
	if len(p.rows) > visibleRows {
		lines = append(lines, fmt.Sprintf("Networks %d–%d of %d", start+1, end, len(p.rows)))
	}
	lines = append(lines, "ctrl+n add  ctrl+d remove  ↑/↓ select  enter continue")
	if p.err != nil {
		lines = append(lines, entryErrorStyle.Render(p.err.Error()))
	}
	return strings.Join(lines, "\n")
}

func (p *prefixInputs) WithTheme(theme huh.Theme) huh.Field {
	p.theme = theme
	p.Input.WithTheme(theme)
	for _, row := range p.rows {
		row.WithTheme(theme)
	}
	return p
}

func (p *prefixInputs) WithKeyMap(keymap *huh.KeyMap) huh.Field {
	p.keymap = keymap
	p.Input.WithKeyMap(keymap)
	for _, row := range p.rows {
		row.WithKeyMap(keymap)
	}
	return p
}

func (p *prefixInputs) WithWidth(width int) huh.Field {
	p.width = width
	p.Input.WithWidth(width)
	for _, row := range p.rows {
		row.WithWidth(max(width-prefixInputChrome, minContentWidth))
	}
	return p
}
func (p *prefixInputs) WithHeight(height int) huh.Field { p.Input.WithHeight(height); return p }
func (p *prefixInputs) WithPosition(position huh.FieldPosition) huh.Field {
	p.Input.WithPosition(position)
	return p
}

func (p *prefixInputs) RunAccessible(w io.Writer, r io.Reader) error {
	for i := 0; i < len(p.rows); i++ { //nolint:intrange // Adding a network extends this loop while it runs.
		if err := p.rows[i].Title(fmt.Sprintf("IPTV network %d (empty to omit)", i+1)).RunAccessible(w, r); err != nil {
			return fmt.Errorf("read IPTV network: %w", err)
		}
		if i == len(p.rows)-1 {
			var add bool
			if err := huh.NewConfirm().Title("Add another network?").Value(&add).RunAccessible(w, r); err != nil {
				return fmt.Errorf("read add-network choice: %w", err)
			}
			if add {
				p.appendRow("")
			}
		}
	}
	p.syncValue()
	return nil
}

func prefixValue(row *huh.Input) string {
	value, _ := row.GetValue().(string)
	return strings.TrimSpace(value)
}

func (p *prefixInputs) Run() error {
	if err := huh.NewForm(huh.NewGroup(p)).Run(); err != nil {
		return fmt.Errorf("edit IPTV networks: %w", err)
	}
	return nil
}
