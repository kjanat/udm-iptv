package ui

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kjanat/udm-iptv/internal/diagnostics"
)

type captureTick time.Time

const (
	defaultViewportWidth  = 80
	defaultViewportHeight = 20
	captureTickInterval   = 500 * time.Millisecond
	minViewportWidth      = 20
	minViewportHeight     = 3
	// chromeHeight is the frame rows the viewport must leave for surrounding chrome.
	chromeHeight = 5
	// statusHeight is the rows the fixed status block takes above the timeline.
	statusHeight = 5
	markerPrompt = "marker: "
)

type captureModel struct {
	capturePath string
	completion  time.Time
	pid         int
	viewport    viewport.Model
	spinner     spinner.Model
	input       textinput.Model
	entering    bool
	structured  bool
	status      []string
	notice      string
	done        bool
	failed      bool
}

// NewCaptureModel builds a Bubble Tea model that follows a diagnostics capture in progress.
func NewCaptureModel(capturePath string, completion time.Time, pid int) tea.Model {
	view := viewport.New(viewport.WithWidth(defaultViewportWidth), viewport.WithHeight(defaultViewportHeight))
	view.SoftWrap = true
	view.FillHeight = true
	progress := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("6"))),
	)
	input := textinput.New()
	input.Prompt = markerPrompt
	input.Placeholder = "TV switched on"

	return captureModel{capturePath: capturePath, completion: completion, pid: pid, viewport: view, spinner: progress, input: input}
}

func (model captureModel) Init() tea.Cmd {
	return tea.Batch(captureTickCommand(), model.spinner.Tick)
}

func captureTickCommand() tea.Cmd {
	return tea.Tick(captureTickInterval, func(value time.Time) tea.Msg { return captureTick(value) })
}

func (model captureModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	switch typed := message.(type) {
	case tea.KeyPressMsg:
		return model.keyPressed(typed)
	case tea.WindowSizeMsg:
		model = model.resized(typed)
	case captureTick:
		model = model.refreshed()
		if model.done || model.failed {
			return model, tea.Quit
		}
		commands = append(commands, captureTickCommand())
	}
	var spinnerCommand tea.Cmd
	model.spinner, spinnerCommand = model.spinner.Update(message)
	updated, command := model.viewport.Update(message)
	model.viewport = updated
	commands = append(commands, spinnerCommand, command)

	return model, tea.Batch(commands...)
}

func (model captureModel) keyPressed(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case model.entering:
		return model.updateMarkerEntry(msg)
	case closesViewer(msg):
		return model, tea.Quit
	case msg.String() == "m" && model.structured && !model.done && !model.failed:
		model.entering = true
		model.input.SetValue("")

		return model, model.input.Focus()
	}
	updated, command := model.viewport.Update(msg)
	model.viewport = updated

	return model, command
}

func (model captureModel) updateMarkerEntry(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		model.entering = false
		model.input.Blur()

		return model, nil
	case "enter":
		model.entering = false
		model.input.Blur()
		text := strings.TrimSpace(model.input.Value())
		if text == "" {
			text = model.input.Placeholder
		}
		if err := diagnostics.WriteMarker(model.capturePath, text, time.Now()); err != nil {
			model.notice = "marker not written: " + err.Error()
		} else {
			model.notice = "marker recorded; it appears with the next sample"
		}

		return model, nil
	}
	var command tea.Cmd
	model.input, command = model.input.Update(msg)

	return model, command
}

func closesViewer(msg tea.KeyPressMsg) bool {
	return msg.String() == "ctrl+c" || msg.String() == "q"
}

func (model captureModel) resized(msg tea.WindowSizeMsg) captureModel {
	model.viewport.SetWidth(max(minViewportWidth, msg.Width))
	model.viewport.SetHeight(max(minViewportHeight, msg.Height-chromeHeight-statusHeight))

	return model
}

func (model captureModel) refreshed() captureModel {
	data, err := os.ReadFile(model.capturePath)
	if err != nil {
		return model
	}
	content := string(data)
	followBottom := model.viewport.AtBottom()
	events := parseCapture(content)
	if len(events) > 0 {
		model.structured = true
		model.viewport.SetContent(strings.Join(timeline(events), "\n"))
		model.status = status(latestSnapshot(events))
		model.done = hasEvent(events, diagnostics.EventCompleted)
		model.failed = hasEvent(events, diagnostics.EventFailed, diagnostics.EventTimeout)
	} else {
		model.viewport.SetContent(content)
		model.done = captureCompleted(content)
		model.failed = captureStopped(content)
	}
	if followBottom {
		model.viewport.GotoBottom()
	}
	if model.completion.IsZero() {
		model.completion = captureCompletion(content)
	}

	return model
}

func latestSnapshot(events []diagnostics.Event) *diagnostics.Snapshot {
	for _, event := range slices.Backward(events) {
		if event.Snapshot != nil {
			return event.Snapshot
		}
	}

	return nil
}

func hasEvent(events []diagnostics.Event, types ...string) bool {
	for _, event := range events {
		if slices.Contains(types, event.Type) {
			return true
		}
	}

	return false
}

func captureCompleted(content string) bool {
	return containsAny(content, "Capture completed:", `"type":"completed"`)
}

func captureStopped(content string) bool {
	return containsAny(content, "Capture failed:", `"type":"failed"`, "Capture timed out:", `"type":"timeout"`)
}

func containsAny(content string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(content, marker) {
			return true
		}
	}

	return false
}

func (model captureModel) header() string {
	remaining := time.Until(model.completion).Round(time.Second)
	if model.completion.IsZero() || remaining < 0 {
		remaining = 0
	}
	state := model.spinner.View() + " capturing"
	if model.done {
		state = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("✓ capture complete")
	} else if model.failed {
		state = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("✗ capture failed")
	}
	identity := ""
	if model.pid > 0 {
		identity = fmt.Sprintf("  PID %d", model.pid)
	}
	keys := "↑/↓ scroll  q/Ctrl-C closes the viewer"
	if model.structured {
		keys = "m marker  " + keys
	}

	return fmt.Sprintf("%s%s  remaining %s  %s", state, identity, remaining, keys)
}

func (model captureModel) View() tea.View {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")).Render("udm-iptv diagnostics")
	header := model.header()
	var body strings.Builder
	body.WriteString(title)
	body.WriteByte('\n')
	body.WriteString(header)
	body.WriteByte('\n')
	if model.structured {
		body.WriteString(lipgloss.NewStyle().Faint(true).Render(strings.Join(model.status, "\n")))
		body.WriteByte('\n')
	}
	body.WriteByte('\n')
	body.WriteString(model.viewport.View())
	body.WriteByte('\n')
	switch {
	case model.entering:
		body.WriteString(model.input.View())
	case model.notice != "":
		body.WriteString(model.notice)
	default:
		body.WriteString(model.capturePath)
	}

	return tea.NewView(body.String())
}

func captureCompletion(content string) time.Time {
	const marker = "expected completion "
	_, after, ok := strings.Cut(content, marker)
	if !ok {
		return time.Time{}
	}
	value := after
	value, _, _ = strings.Cut(value, "\n")
	value, _, _ = strings.Cut(value, `"`)
	parsed, _ := time.Parse(time.RFC3339, strings.TrimSpace(value))

	return parsed
}
