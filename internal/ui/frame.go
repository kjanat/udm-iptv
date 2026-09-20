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
	// preferredContentWidth is the content width used whenever it fits.
	preferredContentWidth = 100
	// contentShare is the share of a wide terminal the content grows to, in percent.
	contentShare = 60
	// maxContentWidth keeps text lines readable on very wide terminals.
	maxContentWidth = 160
	// minContentWidth is the narrowest the content shrinks to while the terminal can fit it.
	minContentWidth = 20

	// panelPaddingY pads the frame and help panel borders vertically.
	panelPaddingY = 1
	// panelPaddingX pads the frame and help panel borders horizontally.
	panelPaddingX = 2
	// borderSize is the width of one border line.
	borderSize = 1
	// frameChromeX is what the frame's left and right border and padding take.
	frameChromeX = 2 * (panelPaddingX + borderSize)
	// frameChromeY is what the frame's top and bottom border and padding take.
	frameChromeY = 2 * (panelPaddingY + borderSize)
	// frameExtraRows is the top row, the spacer before the progress line and the progress line.
	frameExtraRows = 3
	// outerMarginX keeps the frame border clear of the terminal edge.
	outerMarginX = 2

	// popupPaddingY pads the popup border vertically.
	popupPaddingY = 1
	// popupPaddingX pads the popup border horizontally.
	popupPaddingX = 3
	// popupPadding is what the popup's left and right border and padding take.
	popupPadding = 2 * (popupPaddingX + borderSize)
	// hintGap separates the progress bar from the text on either side.
	hintGap = 4
)

var (
	frameStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#444444")).
			Padding(panelPaddingY, panelPaddingX)
	accentStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7571F9"))
	helpKeys    = key.NewBinding(key.WithKeys("f1", "?", "ctrl+_"), key.WithHelp("F1/?", "explain"))
	quitKeys    = key.NewBinding(key.WithKeys("ctrl+c"))
	backKeys    = key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back"))
	closeLabel  = "✕ close"
	leaveLabel  = " Yes, leave "
	stayLabel   = " No, stay "
	popupStyle  = lipgloss.NewStyle().
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
	address *dhcpConfirm
	search  *searchable
	entry   *entryPrompt
	group   *huh.Group
	keys    []string
	hidden  func() bool
}

func newPage(fields ...huh.Field) *page {
	keys := make([]string, 0, len(fields))
	for _, field := range fields {
		keys = append(keys, field.GetKey())
	}

	return &page{group: huh.NewGroup(fields...), keys: keys}
}

func (p *page) title(text string) *page {
	p.group.Title(text)

	return p
}

func (p *page) description(text string) *page {
	p.group.Description(hyperlinkURLs(text))

	return p
}

func (p *page) hide(hidden func() bool) *page {
	p.group.WithHideFunc(hidden)
	p.hidden = hidden

	return p
}

// skip hides the page for good, whatever its own hide condition says.
func (p *page) skip() {
	p.hide(func() bool { return true })
}

func (p *page) searching(search *searchable) *page {
	p.search = search

	return p
}

// entering registers the popup input the page's list opens for its
// "enter manually" row.
func (p *page) entering(entry *entryPrompt) *page {
	p.entry = entry

	return p
}

func (p *page) visible() bool {
	return p.hidden == nil || !p.hidden()
}

func (p *page) contains(key string) bool {
	return slices.Contains(p.keys, key)
}

// ErrBack reports that the user stepped back out of a form's first page.
// The caller re-runs the previous form.
var ErrBack = errors.New("back to the previous form")

// Wizard is one huh form plus the step numbers of the surrounding forms.
type Wizard struct {
	Form          *huh.Form
	pages         []*page
	before, after int
	wentBack      bool
	done          chan struct{}
}

func wizardForm(pages ...*page) *Wizard {
	groups := make([]*huh.Group, 0, len(pages))
	for _, p := range pages {
		groups = append(groups, p.group)
	}
	keymap := huh.NewDefaultKeyMap()
	keymap.Confirm.Next = key.NewBinding(key.WithKeys("enter", "tab"), key.WithHelp("enter/tab", "next"))
	keymap.Confirm.Submit = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter/tab", "submit"))
	for _, p := range pages {
		if p.search != nil {
			keymap.Select.Filter = key.NewBinding(key.WithKeys("/"), key.WithHelp("type", "search"))
		}
	}

	return &Wizard{Form: huh.NewForm(groups...).WithWidth(preferredContentWidth).WithTheme(wizardTheme).WithKeyMap(keymap), pages: pages}
}

func (wizard *Wizard) steps(before, after int) *Wizard {
	wizard.before, wizard.after = before, after

	return wizard
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
			return p.contains(focused)
		}
	}

	return false
}

// atFirstField reports whether focus is on the very first question of the form.
func (wizard *Wizard) atFirstField() bool {
	focused := focusedKey(wizard.Form)
	for _, p := range wizard.pages {
		if p.visible() {
			return len(p.keys) > 0 && p.keys[0] == focused
		}
	}

	return false
}

func (wizard *Wizard) focusedPage() (*page, bool) {
	focused := focusedKey(wizard.Form)
	for _, p := range wizard.pages {
		if p.contains(focused) {
			return p, true
		}
	}

	return nil, false
}

// search returns the searchable list behind the focused field, if any.
func (wizard *Wizard) search() *searchable {
	if p, ok := wizard.focusedPage(); ok {
		return p.search
	}

	return nil
}

// entry returns the popup input behind the focused list, if any.
func (wizard *Wizard) entry() *entryPrompt {
	if p, ok := wizard.focusedPage(); ok {
		return p.entry
	}

	return nil
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

// Event is a wizard interaction worth counting.
type Event string

// The events a Frame reports to its Observer.
const (
	EventHelp       Event = "help"
	EventQuitPrompt Event = "quit.prompt"
	EventAbort      Event = "abort"
	EventBack       Event = "back"
	EventEntryOpen  Event = "entry.open"
)

// Observer receives events with the key of the question that had focus.
type Observer func(event Event, question string)

// Frame owns the screen around a wizard: alternate screen buffer, a bordered
// box centered in the terminal, a progress line and an optional header.
// Input has three modes, in precedence order: the quit prompt, the entry
// popup, and the wizard itself.
type Frame struct {
	wizard         *Wizard
	header         string
	observer       Observer
	width, height  int
	rows           int
	help           bool
	helpOffset     int
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

// Update dispatches on the message kind; anything unknown goes to the form.
func (frame *Frame) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case openEntryMsg:
		frame.openEntry(msg.entry)
		return frame, nil
	case setWizardMsg:
		return frame, frame.setWizard(msg.wizard)
	case wizardDoneMsg:
		frame.finishWizard()

		return frame, nil
	case tea.WindowSizeMsg:
		return frame, frame.handleResize(msg)
	case tea.MouseClickMsg:
		return frame, frame.handleMouse(msg)
	case tea.KeyPressMsg:
		return frame, frame.handleKey(msg)
	default:
		return frame, frame.forward(msg)
	}
}

// setWizard swaps in the next form of the session and lets the box shrink
// to it.
func (frame *Frame) setWizard(wizard *Wizard) tea.Cmd {
	frame.wizard = wizard
	frame.rows = 0
	frame.wizard.Form.WithWidth(frame.contentWidth())

	return tea.Batch(frame.wizard.Form.Init(), frame.resize())
}

func (frame *Frame) finishWizard() {
	if frame.wizard.done != nil {
		close(frame.wizard.done)
		frame.wizard.done = nil
	}
}

func (frame *Frame) handleResize(msg tea.WindowSizeMsg) tea.Cmd {
	frame.width, frame.height = msg.Width, msg.Height
	frame.wizard.Form.WithWidth(frame.contentWidth())

	return frame.resize()
}

// handleMouse treats the quit prompt and the entry popup as modal: clicks
// never reach what lies behind them.
func (frame *Frame) handleMouse(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	if frame.quitPrompt {
		switch {
		case hits(msg.X, msg.Y, frame.leaveX, frame.leaveY, leaveLabel):
			return frame.leave()
		case hits(msg.X, msg.Y, frame.stayX, frame.stayY, stayLabel):
			frame.quitPrompt = false
		}

		return nil
	}
	if frame.entry != nil {
		return nil
	}
	if hits(msg.X, msg.Y, frame.closeX, frame.closeY, closeLabel) {
		return frame.askToLeave()
	}

	return nil
}

type keyPress struct {
	msg   tea.KeyPressMsg
	armed bool
}

type keyRule struct {
	when func(*Frame, keyPress) bool
	then func(*Frame, keyPress) tea.Cmd
}

var frameKeys = []keyRule{
	{(*Frame).showingHelp, (*Frame).closeHelpOn},
	{(*Frame).leaveRequested, (*Frame).askToLeaveOn},
	{(*Frame).escapesQuestion, (*Frame).escapeOn},
	{(*Frame).stepsOutOfForm, (*Frame).goBackOn},
	{(*Frame).explainRequested, (*Frame).toggleHelpOn},
	{(*Frame).rejectedByVLAN, (*Frame).ignoreOn},
}

// handleKey routes a key by mode: quit prompt, entry popup, then the
// frame's own keys, then the form.
func (frame *Frame) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	if handled, cmd := frame.handleModalKey(msg); handled {
		return cmd
	}
	press := keyPress{msg: msg, armed: frame.escArmed}
	frame.escArmed = msg.Code == tea.KeyEscape
	if frame.help {
		return frame.closeHelpOn(press)
	}
	if frame.explainRequested(press) {
		return frame.toggleHelpOn(press)
	}
	if handled, cmd := frame.handleSearchKey(msg); handled {
		return cmd
	}
	for _, rule := range frameKeys {
		if rule.when(frame, press) {
			return rule.then(frame, press)
		}
	}

	return frame.forward(msg)
}

func (frame *Frame) handleModalKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch {
	case frame.quitPrompt:
		frame.escArmed = false

		return true, frame.answerQuitPrompt(msg)
	case frame.entry != nil:
		frame.escArmed = false

		return true, frame.answerEntry(msg)
	case frame.opensManualEntry(msg):
		frame.openEntry(frame.wizard.entry())

		return true, nil
	default:
		return false, nil
	}
}

func (frame *Frame) handleSearchKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	search := frame.wizard.search()
	if search == nil || msg.Mod != 0 {
		return false, nil
	}
	if msg.Text == "/" {
		return true, nil
	}
	if search.keystroke(msg.Text, msg.Code == tea.KeyBackspace, msg.Code == tea.KeyEscape) {
		return true, frame.forward(searchChangedMsg{})
	}

	return false, nil
}

func (frame *Frame) opensManualEntry(msg tea.KeyPressMsg) bool {
	return frame.wizard.entry() != nil && hoversManualEntry(frame.wizard.Form) && opensEntry(msg)
}

func (frame *Frame) leaveRequested(press keyPress) bool {
	return key.Matches(press.msg, quitKeys)
}

func (frame *Frame) askToLeaveOn(keyPress) tea.Cmd {
	return frame.askToLeave()
}

func (frame *Frame) escapesQuestion(press keyPress) bool {
	return press.msg.Code == tea.KeyEscape && !filtering(frame.wizard.Form)
}

func (frame *Frame) escapeOn(press keyPress) tea.Cmd {
	return frame.escape(press.armed)
}

func (frame *Frame) stepsOutOfForm(press keyPress) bool {
	return key.Matches(press.msg, backKeys) && frame.wizard.canBack() && frame.wizard.atFirstField()
}

func (frame *Frame) goBackOn(keyPress) tea.Cmd {
	return frame.goBack()
}

func (frame *Frame) explainRequested(press keyPress) bool {
	return key.Matches(press.msg, helpKeys)
}

func (frame *Frame) toggleHelpOn(keyPress) tea.Cmd {
	frame.help = !frame.help
	frame.helpOffset = 0
	if frame.help {
		frame.observe(EventHelp)
	}

	return nil
}

func (frame *Frame) showingHelp(keyPress) bool {
	return frame.help
}

// closeHelpOn consumes the key, so an escape that closed the help does not
// count as the first of two.
func (frame *Frame) closeHelpOn(press keyPress) tea.Cmd {
	_, lines, height := frame.helpContent()
	switch press.msg.Code {
	case tea.KeyUp:
		frame.helpOffset--
	case tea.KeyDown:
		frame.helpOffset++
	case tea.KeyPgUp:
		frame.helpOffset -= height
	case tea.KeyPgDown:
		frame.helpOffset += height
	case tea.KeyHome:
		frame.helpOffset = 0
	case tea.KeyEnd:
		frame.helpOffset = len(lines)
	default:
		frame.help, frame.escArmed = false, false
	}
	frame.helpOffset = min(max(frame.helpOffset, 0), max(len(lines)-height, 0))

	return nil
}

func (frame *Frame) rejectedByVLAN(press keyPress) bool {
	return press.msg.Text != "" && !isDigits(press.msg.Text) && focusedKey(frame.wizard.Form) == "vlan"
}

func (frame *Frame) ignoreOn(keyPress) tea.Cmd {
	return nil
}

func opensEntry(msg tea.KeyPressMsg) bool {
	return msg.Code == tea.KeyEnter || msg.Code == tea.KeySpace || msg.Text == "x"
}

// hoverable is a huh list whose cursor row can be read.
type hoverable interface {
	Hovered() (string, bool)
	GetFiltering() bool
}

// filterable is a huh field with a type-to-filter mode.
type filterable interface {
	GetFiltering() bool
}

// hoversManualEntry reports whether the focused list's cursor sits on the
// "enter manually" row.
func hoversManualEntry(form *huh.Form) bool {
	field, ok := form.GetFocusedField().(hoverable)
	if !ok || field.GetFiltering() {
		return false
	}
	hovered, ok := field.Hovered()

	return ok && hovered == manualPort
}

func filtering(form *huh.Form) bool {
	field, ok := form.GetFocusedField().(filterable)

	return ok && field.GetFiltering()
}

func (frame *Frame) resize() tea.Cmd {
	if frame.width == 0 {
		return nil
	}
	height := max(1, frame.height-frameChromeY-frameExtraRows)

	return frame.forward(tea.WindowSizeMsg{Width: frame.contentWidth(), Height: height})
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
	frame.observe(EventQuitPrompt)

	return nil
}

func (frame *Frame) leave() tea.Cmd {
	frame.quitPrompt = false
	frame.observe(EventAbort)

	return frame.forward(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
}

// escape steps back one question, or one form on the first question. A
// second escape in a row, or one with nothing to go back to, offers to
// leave.
func (frame *Frame) escape(armed bool) tea.Cmd {
	switch {
	case armed:
		return frame.askToLeave()
	case !frame.wizard.atFirstField():
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
	frame.observe(EventBack)

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

// contentWidth is the width policy: the preferred width when it fits, else
// what fits; on wide terminals grow toward the content share, capped; and
// never wider than the terminal can hold.
func (frame *Frame) contentWidth() int {
	if frame.width == 0 {
		return preferredContentWidth
	}
	available := max(1, frame.width-frameChromeX-outerMarginX)
	preferred := min(preferredContentWidth, available)
	responsive := min(frame.width*contentShare/100, maxContentWidth)
	target := max(minContentWidth, preferred, responsive)

	return min(target, available)
}

func (frame *Frame) render() string {
	page := frame.wizard.Form.View()
	if page == "" {
		return ""
	}
	frame.rows = max(frame.rows, lipgloss.Height(page))
	page = lipgloss.NewStyle().Height(frame.rows).Render(page)
	box := frameStyle.Render(lipgloss.JoinVertical(lipgloss.Left, page, "", frame.progressLine()))
	content := lipgloss.JoinVertical(lipgloss.Left, frame.topRow(lipgloss.Width(box)), box)
	if frame.width > 0 && frame.height > 0 {
		content = lipgloss.Place(frame.width, frame.height, lipgloss.Center, lipgloss.Center, content)
	}
	if frame.help {
		content = frame.overlay(content, frame.helpBox())
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
		accentStyle.Render("Leave the wizard?"),
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
	entry, lines, height := frame.helpContent()
	width := frame.helpWidth()
	start := min(frame.helpOffset, max(len(lines)-height, 0))
	body := strings.Join(lines[start:min(start+height, len(lines))], "\n")
	footer := accentStyle.Render("any key") + progressTextStyle.Render(" back to the question")
	if len(lines) > height {
		footer = progressTextStyle.Render("↑/↓ PgUp/PgDn scroll · Esc close")
	}
	text := lipgloss.JoinVertical(lipgloss.Left, accentStyle.Render(entry.title), "", body)
	return popupStyle.Width(width + popupPadding).Render(lipgloss.JoinVertical(lipgloss.Left, text, "", footer))
}

func (frame *Frame) helpWidth() int {
	return max(1, min(frame.contentWidth()-popupPadding, maxPopupWidth))
}

func (frame *Frame) helpContent() (helpEntry, []string, int) {
	entry, ok := fieldHelp[focusedKey(frame.wizard.Form)]
	if !ok {
		entry = helpEntry{"Help", "No explanation is available for this question."}
	}
	body := lipgloss.NewStyle().Width(frame.helpWidth()).Render(hyperlinkURLs(entry.text))
	lines := strings.Split(body, "\n")
	height := len(lines)
	if frame.height > 0 {
		// Border/padding, title/footer with gaps, and a row outside each edge.
		const helpChrome = 2*(borderSize+popupPaddingY) + 6
		height = max(1, frame.height-helpChrome)
	}
	return entry, lines, height
}

func (frame *Frame) observe(event Event) {
	if frame.observer != nil {
		frame.observer(event, focusedKey(frame.wizard.Form))
	}
}

// progressLine shows the hints, a bar and the question count. When the bar
// has no room it degrades to the count alone.
func (frame *Frame) progressLine() string {
	step, total := frame.wizard.progress()
	text := fmt.Sprintf("Question %d of %d", step, total)
	hint := accentStyle.Render("F1/?") + progressTextStyle.Render(" explain  ") +
		accentStyle.Render("esc") + progressTextStyle.Render(" back  ") +
		accentStyle.Render("esc twice") + progressTextStyle.Render(" leave")
	barWidth := frame.contentWidth() - lipgloss.Width(text) - lipgloss.Width(hint) - hintGap
	if barWidth <= 0 {
		return progressTextStyle.Render(text)
	}
	filled := 0
	if total > 0 {
		filled = min(barWidth*step/total, barWidth)
	}
	bar := progressDoneStyle.Render(strings.Repeat("━", filled)) + progressLeftStyle.Render(strings.Repeat("─", barWidth-filled))

	return hint + "  " + bar + "  " + progressTextStyle.Render(text)
}

// Session keeps one terminal program alive across the forms of a wizard so
// the alternate screen is entered once. The program outlives any single
// Run; each Run watches its own context, and Close stops the program.
type Session struct {
	header   string
	observer Observer
	input    io.Reader
	output   io.Writer
	program  *tea.Program
	// finished closes when the program has exited; err holds its error.
	finished chan struct{}
	err      error
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

// Run shows the wizard and waits for it to finish, or for ctx to end. The
// terminal program is started on the first call; Close stops it.
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
		return fmt.Errorf("show the form: %w", ctx.Err())
	case <-session.finished:
		if session.err == nil || errors.Is(session.err, tea.ErrInterrupted) {
			return huh.ErrUserAborted
		}

		return session.err
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
		_, session.err = session.program.Run()
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
