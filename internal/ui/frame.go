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
	// frameContentWidth is the content width before the terminal size is known.
	frameContentWidth = 100
	// contentShare is the share of the terminal width the content takes, in percent.
	contentShare = 60
	// maxContentWidth keeps text lines readable on very wide terminals.
	maxContentWidth = 160
	// frameChrome is the sum of the frame's top and bottom borders and padding.
	frameChrome = 6
	// panelPaddingY pads the frame and help panel borders.
	panelPaddingY = 1
	// panelPaddingX pads the frame and help panel borders.
	panelPaddingX = 2
	// footerHeight is the status/hint row reserved below the viewport.
	footerHeight = 3
	// minContentWidth keeps narrow terminals from collapsing the frame further.
	minContentWidth = 20
	// hintGap separates a footer's text from its trailing hint.
	hintGap = 4
	// outerMarginX keeps the frame border clear of the terminal edge.
	outerMarginX = 2
)

var (
	frameStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#444444")).
			Padding(panelPaddingY, panelPaddingX)
	helpStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7571F9")).
			Padding(panelPaddingY, panelPaddingX)
	helpTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7571F9"))
	helpKeyStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7571F9"))
	helpKeys       = key.NewBinding(key.WithKeys("f1", "ctrl+_"), key.WithHelp("F1", "explain"))
	quitKeys       = key.NewBinding(key.WithKeys("ctrl+c", "esc"))
	closeLabel     = "✕ close"
	leaveLabel     = " Yes, leave "
	stayLabel      = " No, stay "
	popupStyle     = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(lipgloss.Color("#F780E2")).
			Padding(1, 3)
	focusedButtonStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFDF5")).Background(lipgloss.Color("#F780E2"))
	blurredButtonStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFDF5")).Background(lipgloss.Color("#444444"))
	progressDoneStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#7571F9"))
	progressLeftStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#444444"))
	progressTextStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
	badgeStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#1A1A1A")).Background(lipgloss.Color("#F7C948")).Padding(0, 1)
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
	wizard         *Wizard
	header         string
	observer       Observer
	width, height  int
	rows           int
	help           bool
	quitPrompt     bool
	stayFocused    bool
	closeX, closeY int
	leaveX, leaveY int
	stayX, stayY   int
}

type setWizardMsg struct{ wizard *Wizard }

type wizardDoneMsg struct{}

// NewFrame wraps wizard in a bordered, resizable Bubble Tea screen.
func NewFrame(wizard *Wizard, header string) *Frame {
	return &Frame{wizard: wizard, header: header}
}

// Init starts the wrapped wizard's form.
func (frame *Frame) Init() tea.Cmd {
	return frame.wizard.Form.Init()
}

// Update handles resize, help, quit and quit-confirmation, forwarding
// everything else to the wrapped wizard.
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
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return frame, nil
		}
		switch {
		case frame.quitPrompt && hits(msg.X, msg.Y, frame.leaveX, frame.leaveY, leaveLabel):
			return frame, frame.leave()
		case frame.quitPrompt && hits(msg.X, msg.Y, frame.stayX, frame.stayY, stayLabel):
			frame.quitPrompt = false
		case hits(msg.X, msg.Y, frame.closeX, frame.closeY, closeLabel):
			return frame, frame.askToLeave()
		}

		return frame, nil
	case tea.KeyPressMsg:
		if frame.quitPrompt {
			return frame, frame.answerQuitPrompt(msg)
		}
		if key.Matches(msg, quitKeys) && !filtering(frame.wizard.Form) {
			return frame, frame.askToLeave()
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
	height := frame.height - frameChrome - footerHeight

	return frame.forward(tea.WindowSizeMsg{Width: frame.contentWidth(), Height: max(height, 1)})
}

func (frame *Frame) forward(msg tea.Msg) tea.Cmd {
	model, cmd := frame.wizard.Form.Update(msg)
	if form, ok := model.(*huh.Form); ok {
		frame.wizard.Form = form
	}

	return cmd
}

// View renders the wizard inside its bordered frame, with an optional
// header, progress line, help panel and quit confirmation.
func (frame *Frame) View() tea.View {
	view := tea.NewView(frame.render())
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion

	return view
}

// The first request opens the popup with "Yes, leave" focused; the second
// one, or Enter, leaves the form.
func (frame *Frame) askToLeave() tea.Cmd {
	if frame.quitPrompt {
		return frame.leave()
	}
	frame.help, frame.quitPrompt, frame.stayFocused = false, true, false
	frame.observe("quit.prompt")

	return nil
}

func (frame *Frame) leave() tea.Cmd {
	frame.quitPrompt = false
	frame.observe("abort")

	return frame.forward(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
}

func (frame *Frame) answerQuitPrompt(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case msg.Code == tea.KeyEscape, msg.Text == "n", msg.Text == "N":
		frame.quitPrompt = false
	case key.Matches(msg, quitKeys), msg.Text == "y", msg.Text == "Y":
		return frame.leave()
	case msg.Code == tea.KeyEnter:
		if frame.stayFocused {
			frame.quitPrompt = false

			return nil
		}

		return frame.leave()
	case msg.Code == tea.KeyLeft, msg.Code == tea.KeyRight, msg.Code == tea.KeyTab, msg.Text == "h", msg.Text == "l":
		frame.stayFocused = !frame.stayFocused
	}

	return nil
}

func hits(x, y, atX, atY int, label string) bool {
	return atY >= 0 && y == atY && x >= atX && x < atX+lipgloss.Width(label)
}

func filtering(form *huh.Form) bool {
	switch field := form.GetFocusedField().(type) {
	case *huh.Select[string]:
		return field.GetFiltering()
	case *huh.Select[int]:
		return field.GetFiltering()
	case *huh.MultiSelect[string]:
		return field.GetFiltering()
	default:
		return false
	}
}

func (frame *Frame) contentWidth() int {
	if frame.width == 0 {
		return frameContentWidth
	}

	available := frame.width - frameChrome - outerMarginX
	share := min(frame.width*contentShare/100, maxContentWidth)
	width := max(min(available, frameContentWidth), share)

	return max(min(width, available), minContentWidth)
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
	content := lipgloss.JoinVertical(lipgloss.Left, frame.topRow(lipgloss.Width(box)), box)
	if frame.width > 0 && frame.height > 0 {
		content = lipgloss.Place(frame.width, frame.height, lipgloss.Center, lipgloss.Center, content)
	}
	if frame.quitPrompt {
		content = frame.overlay(content, frame.quitPopup())
	}
	frame.closeX, frame.closeY = locate(content, closeLabel)
	frame.leaveX, frame.leaveY = locate(content, leaveLabel)
	frame.stayX, frame.stayY = locate(content, stayLabel)

	return content
}

// overlay draws the popup centered on top of the content.
func (frame *Frame) overlay(content, popup string) string {
	width, height := lipgloss.Width(content), lipgloss.Height(content)
	x := max(0, (width-lipgloss.Width(popup))/2)
	y := max(0, (height-lipgloss.Height(popup))/2)
	canvas := lipgloss.NewCanvas(width, height)
	canvas.Compose(lipgloss.NewCompositor(lipgloss.NewLayer(content), lipgloss.NewLayer(popup).X(x).Y(y).Z(1)))

	return canvas.Render()
}

// topRow puts the badge, when there is one, on the left and the close
// button on the right, on the row above the box.
func (frame *Frame) topRow(width int) string {
	close := progressTextStyle.Render(closeLabel)
	if frame.header == "" {
		return lipgloss.PlaceHorizontal(width, lipgloss.Right, close)
	}
	badge := badgeStyle.Render(strings.ToUpper(frame.header))
	gap := max(1, width-lipgloss.Width(badge)-lipgloss.Width(close))

	return badge + strings.Repeat(" ", gap) + close
}

func (frame *Frame) quitPopup() string {
	leave, stay := focusedButtonStyle, blurredButtonStyle
	if frame.stayFocused {
		leave, stay = blurredButtonStyle, focusedButtonStyle
	}
	buttons := leave.Render(leaveLabel) + "   " + stay.Render(stayLabel)
	hint := progressTextStyle.Render("←/→ choose  enter confirm  y/n  esc back")

	return popupStyle.Render(lipgloss.JoinVertical(lipgloss.Center,
		helpTitleStyle.Render("Leave the wizard?"),
		"",
		"Nothing has been saved. The current configuration stays as it is.",
		"",
		buttons,
		"",
		hint,
	))
}

func locate(content, text string) (x, y int) {
	for y, line := range strings.Split(content, "\n") {
		if index := strings.Index(line, text); index >= 0 {
			return lipgloss.Width(line[:index]), y
		}
	}

	return -1, -1
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

func (frame *Frame) observe(event string) {
	if frame.observer != nil {
		frame.observer(event, focusedKey(frame.wizard.Form))
	}
}

func (frame *Frame) progressLine() string {
	step, total := frame.wizard.progress()
	hint := helpKeyStyle.Render("F1") + progressTextStyle.Render(" explain  ") + helpKeyStyle.Render("esc") + progressTextStyle.Render(" leave")
	text := fmt.Sprintf("Question %d of %d", step, total)
	width := frame.contentWidth() - lipgloss.Width(text) - lipgloss.Width(hint) - hintGap
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

// NewSession creates a terminal session for running one or more wizards
// against the same alternate screen.
func NewSession(header string, input io.Reader, output io.Writer) *Session {
	return &Session{header: header, input: input, output: output}
}

// Observe reports wizard interactions to the observer. Pass it before Run.
func (session *Session) Observe(observer Observer) *Session {
	session.observer = observer

	return session
}

// Run displays wizard until it is submitted or aborted, reusing the session's
// alternate screen across successive wizards.
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
