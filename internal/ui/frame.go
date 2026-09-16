package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
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
	// panelPaddingY pads the frame and help panel borders.
	panelPaddingY = 1
	// panelPaddingX pads the frame and help panel borders.
	panelPaddingX = 2
	// frameChromeY is what the frame's top and bottom border and padding take.
	frameChromeY = 2 * (panelPaddingY + 1)
	// frameChromeX is what the frame's left and right border and padding take.
	frameChromeX = 2 * (panelPaddingX + 1)
	// footerHeight is the status/hint row reserved below the viewport.
	footerHeight = 3
	// minContentWidth keeps narrow terminals from collapsing the frame further.
	minContentWidth = 20
	// popupPaddingY pads the popup border vertically.
	popupPaddingY = 1
	// popupPaddingX pads the popup border horizontally.
	popupPaddingX = 3
	// popupPadding is the horizontal frame popupStyle adds around its content:
	// padding and border on both sides.
	popupPadding = 2 * (popupPaddingX + 1)
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
	quitKeys       = key.NewBinding(key.WithKeys("ctrl+c"))
	backKeys       = key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back"))
	closeLabel     = "✕ close"
	leaveLabel     = " Yes, leave "
	stayLabel      = " No, stay "
	popupStyle     = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(lipgloss.Color("#F780E2")).
			Padding(popupPaddingY, popupPaddingX)
	focusedButtonStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFDF5")).Background(lipgloss.Color("#F780E2"))
	blurredButtonStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFDF5")).Background(lipgloss.Color("#444444"))
	progressDoneStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#7571F9"))
	progressLeftStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#444444"))
	progressTextStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
	badgeStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#1A1A1A")).Background(lipgloss.Color("#F7C948")).Padding(0, 1)
)

var wizardTheme = huh.ThemeFunc(func(isDark bool) *huh.Styles {
	styles := huh.ThemeCharm(isDark)
	lightDark := lipgloss.LightDark(isDark)
	accent := lightDark(lipgloss.Color("#5A56E0"), lipgloss.Color("#7571F9"))
	dim := lightDark(lipgloss.Color("245"), lipgloss.Color("240"))
	dimmer := lightDark(lipgloss.Color("250"), lipgloss.Color("237"))
	styles.Group.Title = styles.Group.Title.Bold(true).Transform(strings.ToUpper).MarginBottom(1)
	styles.Group.Description = styles.Group.Description.MarginBottom(1)
	styles.Focused.Base = styles.Focused.Base.BorderForeground(accent)
	styles.Focused.SelectedPrefix = styles.Focused.SelectedPrefix.SetString("[x] ")
	styles.Focused.UnselectedPrefix = styles.Focused.UnselectedPrefix.SetString("[ ] ")

	blurred := &styles.Blurred
	blurred.Title = blurred.Title.Foreground(dim).Bold(false)
	blurred.Description = blurred.Description.Foreground(dimmer)
	blurred.Option = blurred.Option.Foreground(dim)
	blurred.SelectedOption = blurred.SelectedOption.Foreground(dim)
	blurred.UnselectedOption = blurred.UnselectedOption.Foreground(dim)
	blurred.SelectedPrefix = styles.Focused.SelectedPrefix.Foreground(dim)
	blurred.UnselectedPrefix = styles.Focused.UnselectedPrefix.Foreground(dim)
	blurred.SelectSelector = blurred.SelectSelector.Foreground(dim)
	blurred.MultiSelectSelector = blurred.MultiSelectSelector.Foreground(dim)
	blurred.FocusedButton = blurred.BlurredButton.Foreground(dim).Background(dimmer)
	blurred.BlurredButton = blurred.FocusedButton
	blurred.TextInput.Prompt = blurred.TextInput.Prompt.Foreground(dim)
	blurred.TextInput.Text = blurred.TextInput.Text.Foreground(dim)
	blurred.TextInput.Placeholder = blurred.TextInput.Placeholder.Foreground(dimmer)

	return styles
})

// page pairs a huh group with the field keys it owns and its hide condition,
// which huh does not expose back to callers.
type page struct {
	search *searchable
	entry  *entryPrompt
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

func (p page) searching(s *searchable) page {
	p.search = s

	return p
}

// entering registers the popup input the page's list opens for its
// "enter manually" row.
func (p page) entering(entry *entryPrompt) page {
	p.entry = entry

	return p
}

// ErrBack reports that the user stepped back out of a form's first page.
// The caller re-runs the previous form.
var ErrBack = errors.New("back to the previous form")

// Wizard is one huh form plus the step numbers of the surrounding forms.
type Wizard struct {
	Form          *huh.Form
	pages         []page
	before, after int
	wentBack      bool
	done          chan struct{}
}

// canBack reports whether a form precedes this one.
func (wizard *Wizard) canBack() bool {
	return wizard.before > 0
}

// onFirstPage reports whether the focused field sits on the first visible page.
func (wizard *Wizard) onFirstPage() bool {
	focused := focusedKey(wizard.Form)
	for _, p := range wizard.pages {
		if p.visible() {
			return slices.Contains(p.keys, focused)
		}
	}

	return false
}

// search returns the searchable list behind the focused field, if any.
func (wizard *Wizard) search() *searchable {
	if p, ok := wizard.focusedPage(); ok {
		return p.search
	}

	return nil
}

func (wizard *Wizard) focusedPage() (page, bool) {
	focused := focusedKey(wizard.Form)
	for _, p := range wizard.pages {
		if slices.Contains(p.keys, focused) {
			return p, true
		}
	}

	return page{}, false
}

// entry returns the popup input behind the focused list, if any.
func (wizard *Wizard) entry() *entryPrompt {
	if p, ok := wizard.focusedPage(); ok {
		return p.entry
	}

	return nil
}

func wizardForm(pages ...page) *Wizard {
	groups := make([]*huh.Group, 0, len(pages))
	for _, p := range pages {
		groups = append(groups, p.group)
	}

	keymap := huh.NewDefaultKeyMap()
	for _, p := range pages {
		if p.search != nil {
			keymap.Select.Filter = key.NewBinding(key.WithKeys("/"), key.WithHelp("type", "search"))
		}
	}

	return &Wizard{Form: huh.NewForm(groups...).WithWidth(frameContentWidth).WithTheme(wizardTheme).WithKeyMap(keymap), pages: pages}
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
	escArmed       bool
	entry          *entryPrompt
	entryText      string
	entryCursor    int
	entryErr       error
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
			frame.escArmed = false

			return frame, frame.answerQuitPrompt(msg)
		}
		if frame.entry != nil {
			frame.escArmed = false

			return frame, frame.answerEntry(msg.Text, msg.Code == tea.KeyEnter, msg.Code == tea.KeyBackspace, msg.Code == tea.KeyEscape,
				msg.Code == tea.KeyUp, msg.Code == tea.KeyDown)
		}
		if entry := frame.wizard.entry(); entry != nil && hoversManualEntry(frame.wizard.Form) &&
			(msg.Code == tea.KeyEnter || msg.Code == tea.KeySpace || msg.Text == "x") {
			frame.openEntry(entry)

			return frame, nil
		}
		armed := frame.escArmed
		frame.escArmed = msg.Code == tea.KeyEscape
		if search := frame.wizard.search(); search != nil && msg.Mod == 0 {
			if msg.Text == "/" {
				return frame, nil
			}
			if search.keystroke(msg.Text, msg.Code == tea.KeyBackspace, msg.Code == tea.KeyEscape) {
				return frame, frame.forward(searchChangedMsg{})
			}
		}
		if key.Matches(msg, quitKeys) {
			return frame, frame.askToLeave()
		}
		if msg.Code == tea.KeyEscape && !filtering(frame.wizard.Form) {
			return frame, frame.escape(armed)
		}
		if key.Matches(msg, backKeys) && frame.wizard.canBack() && frame.wizard.onFirstPage() {
			return frame, frame.goBack()
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
	}

	return frame, frame.forward(msg)
}

// hoversManualEntry reports whether the focused list's cursor sits on the
// "enter manually" row.
func hoversManualEntry(form *huh.Form) bool {
	switch field := form.GetFocusedField().(type) {
	case *huh.Select[string]:
		hovered, ok := field.Hovered()

		return ok && !field.GetFiltering() && hovered == manualPort
	case *huh.MultiSelect[string]:
		hovered, ok := field.Hovered()

		return ok && !field.GetFiltering() && hovered == manualPort
	default:
		return false
	}
}

func (frame *Frame) resize() tea.Cmd {
	if frame.width == 0 {
		return nil
	}
	height := frame.height - frameChromeY - footerHeight

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

// escape steps back one page, or one form on the first page. A second
// escape in a row, or one with nothing to go back to, offers to leave.
func (frame *Frame) escape(armed bool) tea.Cmd {
	switch {
	case armed:
		return frame.askToLeave()
	case !frame.wizard.onFirstPage():
		return frame.forward(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	case frame.wizard.canBack():
		return frame.goBack()
	default:
		return frame.askToLeave()
	}
}

// goBack ends the current form so the session re-runs the previous one.
func (frame *Frame) goBack() tea.Cmd {
	frame.wizard.wentBack = true
	frame.observe("back")

	return func() tea.Msg { return wizardDoneMsg{} }
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

	available := frame.width - frameChromeX - outerMarginX
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
	if frame.entry != nil {
		content = frame.overlay(content, frame.entryPopup())
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
	closeButton := progressTextStyle.Render(closeLabel)
	if frame.header == "" {
		return lipgloss.PlaceHorizontal(width, lipgloss.Right, closeButton)
	}
	badge := badgeStyle.Render(strings.ToUpper(frame.header))
	gap := max(1, width-lipgloss.Width(badge)-lipgloss.Width(closeButton))

	return badge + strings.Repeat(" ", gap) + closeButton
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

// locate returns the column and row where text first appears, or -1, -1.
func locate(content, text string) (int, int) {
	for row, line := range strings.Split(content, "\n") {
		if before, _, ok := strings.Cut(line, text); ok {
			return lipgloss.Width(before), row
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
	hint := helpKeyStyle.Render("F1") + progressTextStyle.Render(" explain  ") +
		helpKeyStyle.Render("esc") + progressTextStyle.Render(" back  ") +
		helpKeyStyle.Render("esc twice") + progressTextStyle.Render(" leave")
	text := fmt.Sprintf("Question %d of %d", step, total)
	width := max(frame.contentWidth()-lipgloss.Width(text)-lipgloss.Width(hint)-hintGap, 0)
	filled := 0
	if total > 0 {
		filled = min(width*step/total, width)
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
	// finished closes when the program has exited; runErr holds its error.
	finished chan struct{}
	runErr   error
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
// Run shows the wizard and waits for it to finish. The terminal program is
// started on the first call and outlives the call's context: every Run
// watches its own ctx, and Close stops the program.
func (session *Session) Run(ctx context.Context, wizard *Wizard) error {
	wizard.done = make(chan struct{})
	wizard.Form.SubmitCmd = func() tea.Msg { return wizardDoneMsg{} }
	wizard.Form.CancelCmd = wizard.Form.SubmitCmd
	if session.program == nil {
		session.start(wizard)
	} else {
		session.program.Send(setWizardMsg{wizard: wizard})
	}
	select {
	case <-wizard.done:
	case <-ctx.Done():
		return ctx.Err()
	case <-session.finished:
		if session.runErr == nil || errors.Is(session.runErr, tea.ErrInterrupted) {
			return huh.ErrUserAborted
		}

		return session.runErr
	}
	if wizard.wentBack {
		return ErrBack
	}
	if wizard.Form.State == huh.StateAborted {
		return huh.ErrUserAborted
	}

	return nil
}

func (session *Session) start(wizard *Wizard) {
	frame := NewFrame(wizard, session.header)
	frame.observer = session.observer
	session.program = tea.NewProgram(frame, tea.WithInput(session.input), tea.WithOutput(session.output))
	session.finished = make(chan struct{})
	go func() {
		_, session.runErr = session.program.Run()
		close(session.finished)
	}()
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
