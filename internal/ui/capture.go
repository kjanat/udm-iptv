package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
)

type captureModel struct {
	capturePath string
	completion  time.Time
	pid         int
	viewport    viewport.Model
	spinner     spinner.Model
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

	return captureModel{capturePath: capturePath, completion: completion, pid: pid, viewport: view, spinner: progress}
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
		if closesViewer(typed) {
			return model, tea.Quit
		}
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

func closesViewer(msg tea.KeyPressMsg) bool {
	return msg.String() == "ctrl+c" || msg.String() == "q"
}

func (model captureModel) resized(msg tea.WindowSizeMsg) captureModel {
	model.viewport.SetWidth(max(minViewportWidth, msg.Width))
	model.viewport.SetHeight(max(minViewportHeight, msg.Height-chromeHeight))

	return model
}

func (model captureModel) refreshed() captureModel {
	data, err := os.ReadFile(model.capturePath)
	if err != nil {
		return model
	}
	content := string(data)
	followBottom := model.viewport.AtBottom()
	model.viewport.SetContent(content)
	if followBottom {
		model.viewport.GotoBottom()
	}
	model.done = captureCompleted(content)
	model.failed = captureStopped(content)
	if model.completion.IsZero() {
		model.completion = captureCompletion(content)
	}

	return model
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

func (model captureModel) View() tea.View {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")).Render("udm-iptv diagnostics")
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
	status := fmt.Sprintf("%s%s  remaining %s  ↑/↓ scroll  q/Ctrl-C closes the viewer", state, identity, remaining)
	view := tea.NewView(title + "\n" + status + "\n\n" + model.viewport.View() + "\n" + model.capturePath)

	return view
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
