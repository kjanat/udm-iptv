package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

const (
	frameContentWidth = 100
	frameChrome       = 6
)

var (
	frameStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#444444")).
			Padding(1, 2)
	helpStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7571F9")).
			Padding(1, 2)
	helpTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7571F9"))
	helpKeyStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7571F9"))
	helpKeys          = key.NewBinding(key.WithKeys("f1", "ctrl+_"), key.WithHelp("F1", "explain"))
	quitKeys          = key.NewBinding(key.WithKeys("ctrl+c"))
	progressDoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#7571F9"))
	progressLeftStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#444444"))
	progressTextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
)

var wizardTheme = huh.ThemeFunc(func(isDark bool) *huh.Styles {
	styles := huh.ThemeCharm(isDark)
	styles.Group.Title = styles.Group.Title.Bold(true).Transform(strings.ToUpper).MarginBottom(1)
	styles.Group.Description = styles.Group.Description.MarginBottom(1)
	styles.Focused.SelectedPrefix = styles.Focused.SelectedPrefix.SetString("[x] ")
	styles.Focused.UnselectedPrefix = styles.Focused.UnselectedPrefix.SetString("[ ] ")
	styles.Blurred.SelectedPrefix = styles.Focused.SelectedPrefix
	styles.Blurred.UnselectedPrefix = styles.Focused.UnselectedPrefix

	return styles
})

// page pairs a huh group with the field keys it owns and its hide condition,
// which huh does not expose back to callers.
type page struct {
	group  *huh.Group
	keys   []string
	hidden func() bool
}

func newPage(fields ...huh.Field) page {
	keys := make([]string, 0, len(fields))
	for _, field := range fields {
		keys = append(keys, field.GetKey())
	}

	return page{group: huh.NewGroup(fields...), keys: keys}
}

func (p page) title(text string) page {
	p.group.Title(text)

	return p
}

func (p page) description(text string) page {
	p.group.Description(text)

	return p
}

func (p page) hide(hidden func() bool) page {
	p.group.WithHideFunc(hidden)
	p.hidden = hidden

	return p
}

func (p page) visible() bool {
	return p.hidden == nil || !p.hidden()
}

// Wizard is one huh form plus the step numbers of the surrounding forms.
type Wizard struct {
	Form          *huh.Form
	pages         []page
	before, after int
	done          chan struct{}
}

func wizardForm(pages ...page) *Wizard {
	groups := make([]*huh.Group, 0, len(pages))
	for _, p := range pages {
		groups = append(groups, p.group)
	}

	return &Wizard{Form: huh.NewForm(groups...).WithWidth(frameContentWidth).WithTheme(wizardTheme), pages: pages}
}

func (wizard *Wizard) steps(before, after int) *Wizard {
	wizard.before, wizard.after = before, after

	return wizard
}

func (wizard *Wizard) visiblePages() int {
	count := 0
	for _, p := range wizard.pages {
		if p.visible() {
			count++
		}
	}

	return count
}

func (wizard *Wizard) visibleFields() int {
	count := 0
	for _, p := range wizard.pages {
		if p.visible() {
			count += len(p.keys)
		}
	}

	return count
}

// progress counts questions, one per field, across the surrounding forms.
func (wizard *Wizard) progress() (int, int) {
	total := wizard.before + wizard.visibleFields() + wizard.after
	step := wizard.before
	focused := focusedKey(wizard.Form)
	for _, p := range wizard.pages {
		if !p.visible() {
			continue
		}
		for _, key := range p.keys {
			step++
			if key == focused {
				return step, total
			}
		}
	}

	return step, total
}

// RunForm supplies terminal input, output and execution to the wizard.
type RunForm func(context.Context, *Wizard) error

// Observer receives wizard interactions worth counting: "help", "quit.prompt"
// and "abort", with the key of the question that had focus.
type Observer func(event, question string)

// Frame owns the screen around a wizard: alternate screen buffer, a bordered
// box centered in the terminal, a progress line and an optional header.
type Frame struct {
	wizard        *Wizard
	header        string
	observer      Observer
	width, height int
	rows          int
	help          bool
	quitPrompt    bool
}

type setWizardMsg struct{ wizard *Wizard }

type wizardDoneMsg struct{}

func NewFrame(wizard *Wizard, header string) *Frame {
	return &Frame{wizard: wizard, header: header}
}

func (frame *Frame) Init() tea.Cmd {
	return frame.wizard.Form.Init()
}

func (frame *Frame) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case setWizardMsg:
		frame.wizard = msg.wizard
		frame.wizard.Form.WithWidth(frame.contentWidth())
		init := frame.wizard.Form.Init()

		return frame, tea.Batch(init, frame.resize())
	case wizardDoneMsg:
		if frame.wizard.done != nil {
			close(frame.wizard.done)
			frame.wizard.done = nil
		}

		return frame, nil
	case tea.WindowSizeMsg:
		frame.width, frame.height = msg.Width, msg.Height
		frame.wizard.Form.WithWidth(frame.contentWidth())

		return frame, frame.resize()
	case tea.KeyPressMsg:
		if key.Matches(msg, quitKeys) {
			if frame.quitPrompt {
				frame.observe("abort")

				return frame, frame.forward(msg)
			}
			frame.help, frame.quitPrompt = false, true
			frame.observe("quit.prompt")

			return frame, nil
		}
		if frame.quitPrompt {
			frame.quitPrompt = false

			return frame, nil
		}
		if key.Matches(msg, helpKeys) {
			frame.help = !frame.help
			if frame.help {
				frame.observe("help")
			}

			return frame, nil
		}
		if frame.help {
			frame.help = false

			return frame, nil
		}
		if msg.Text != "" && !isDigits(msg.Text) && focusedKey(frame.wizard.Form) == "vlan" {
			return frame, nil
		}
		if msg.Code == tea.KeyEnter && hoversUntickedManualEntry(frame.wizard.Form) {
			return frame, tea.Batch(frame.forward(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}), frame.forward(msg))
		}
	}

	return frame, frame.forward(msg)
}

// Enter on the "enter manually" row means "I want that", so tick it before
// the form moves on to the manual input page.
func hoversUntickedManualEntry(form *huh.Form) bool {
	field, ok := form.GetFocusedField().(*huh.MultiSelect[string])
	if !ok || field.GetFiltering() {
		return false
	}
	hovered, ok := field.Hovered()
	if !ok || hovered != manualPort {
		return false
	}
	selected, ok := field.GetValue().([]string)

	return ok && !containsString(selected, manualPort)
}

func (frame *Frame) resize() tea.Cmd {
	if frame.width == 0 {
		return nil
	}
	height := frame.height - frameChrome - lipgloss.Height(frame.header) - 3

	return frame.forward(tea.WindowSizeMsg{Width: frame.contentWidth(), Height: max(height, 1)})
}

func (frame *Frame) forward(msg tea.Msg) tea.Cmd {
	model, cmd := frame.wizard.Form.Update(msg)
	if form, ok := model.(*huh.Form); ok {
		frame.wizard.Form = form
	}

	return cmd
}

func (frame *Frame) View() tea.View {
	view := tea.NewView(frame.render())
	view.AltScreen = true

	return view
}

func (frame *Frame) contentWidth() int {
	if frame.width == 0 {
		return frameContentWidth
	}

	return max(min(frame.width-frameChrome-2, frameContentWidth), 20)
}

func (frame *Frame) render() string {
	page := frame.wizard.Form.View()
	if page == "" {
		return ""
	}
	frame.rows = max(frame.rows, lipgloss.Height(page))
	page = lipgloss.NewStyle().Height(frame.rows).Render(page)
	box := frameStyle.Render(lipgloss.JoinVertical(lipgloss.Left, page, "", frame.progressLine()))
	if frame.help {
		box = frame.helpBox()
	}
	if frame.quitPrompt {
		box = frame.quitBox()
	}
	content := box
	if frame.header != "" {
		content = lipgloss.JoinVertical(lipgloss.Center, frame.header, "", box)
	}
	if frame.width == 0 || frame.height == 0 {
		return content
	}

	return lipgloss.Place(frame.width, frame.height, lipgloss.Center, lipgloss.Center, content)
}

func (frame *Frame) helpBox() string {
	entry, ok := fieldHelp[focusedKey(frame.wizard.Form)]
	if !ok {
		entry = helpEntry{"Help", "No explanation is available for this question."}
	}
	width := frame.contentWidth()
	body := lipgloss.NewStyle().Width(width).Render(entry.text)
	footer := helpKeyStyle.Render("any key") + progressTextStyle.Render(" back to the question")
	text := lipgloss.JoinVertical(lipgloss.Left, helpTitleStyle.Render(entry.title), "", body)
	text = lipgloss.NewStyle().Width(width).Height(frame.rows).Render(text)

	return helpStyle.Render(lipgloss.JoinVertical(lipgloss.Left, text, "", footer))
}

func (frame *Frame) quitBox() string {
	width := frame.contentWidth()
	body := lipgloss.NewStyle().Width(width).Render("Nothing has been saved. Leaving now keeps the current configuration untouched.")
	footer := helpKeyStyle.Render("ctrl+c") + progressTextStyle.Render(" again to leave  ") + helpKeyStyle.Render("any other key") + progressTextStyle.Render(" to stay")
	text := lipgloss.JoinVertical(lipgloss.Left, helpTitleStyle.Render("Leave the wizard?"), "", body)
	text = lipgloss.NewStyle().Width(width).Height(frame.rows).Render(text)

	return helpStyle.Render(lipgloss.JoinVertical(lipgloss.Left, text, "", footer))
}

func (frame *Frame) observe(event string) {
	if frame.observer != nil {
		frame.observer(event, focusedKey(frame.wizard.Form))
	}
}

func (frame *Frame) progressLine() string {
	step, total := frame.wizard.progress()
	hint := helpKeyStyle.Render("F1") + progressTextStyle.Render(" explain")
	text := fmt.Sprintf("Question %d of %d", step, total)
	width := frame.contentWidth() - lipgloss.Width(text) - lipgloss.Width(hint) - 4
	filled := 0
	if total > 0 {
		filled = width * step / total
	}
	bar := progressDoneStyle.Render(strings.Repeat("━", filled)) + progressLeftStyle.Render(strings.Repeat("─", width-filled))

	return hint + "  " + bar + "  " + progressTextStyle.Render(text)
}

// Session keeps one terminal program alive across the forms of a wizard so
// the alternate screen is entered once.
type Session struct {
	header   string
	observer Observer
	input    io.Reader
	output   io.Writer
	program  *tea.Program
	finished chan error
	closed   sync.Once
}

func NewSession(header string, input io.Reader, output io.Writer) *Session {
	return &Session{header: header, input: input, output: output}
}

// Observe reports wizard interactions to the observer. Pass it before Run.
func (session *Session) Observe(observer Observer) *Session {
	session.observer = observer

	return session
}

func (session *Session) Run(ctx context.Context, wizard *Wizard) error {
	wizard.done = make(chan struct{})
	wizard.Form.SubmitCmd = func() tea.Msg { return wizardDoneMsg{} }
	wizard.Form.CancelCmd = wizard.Form.SubmitCmd
	if session.program == nil {
		frame := NewFrame(wizard, session.header)
		frame.observer = session.observer
		session.program = tea.NewProgram(frame, tea.WithContext(ctx), tea.WithInput(session.input), tea.WithOutput(session.output))
		session.finished = make(chan error, 1)
		go func() {
			_, err := session.program.Run()
			session.finished <- err
		}()
	} else {
		session.program.Send(setWizardMsg{wizard: wizard})
	}
	select {
	case <-wizard.done:
	case err := <-session.finished:
		session.finished <- err
		if err == nil || errors.Is(err, tea.ErrInterrupted) {
			return huh.ErrUserAborted
		}

		return err
	}
	if wizard.Form.State == huh.StateAborted {
		return huh.ErrUserAborted
	}

	return nil
}

// Close leaves the alternate screen. It is safe to call more than once.
func (session *Session) Close() {
	session.closed.Do(func() {
		if session.program == nil {
			return
		}
		session.program.Quit()
		<-session.finished
	})
}

func focusedKey(form *huh.Form) string {
	field := form.GetFocusedField()
	if field == nil {
		return ""
	}

	return field.GetKey()
}

func isDigits(text string) bool {
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}
