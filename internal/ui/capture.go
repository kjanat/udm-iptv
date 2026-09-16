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

type captureModel struct {
	capturePath string
	completion  time.Time
	pid         int
	viewport    viewport.Model
	spinner     spinner.Model
	done        bool
	failed      bool
}

func NewCaptureModel(capturePath string, completion time.Time, pid int) captureModel {
	view := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))
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
	return tea.Tick(500*time.Millisecond, func(value time.Time) tea.Msg { return captureTick(value) })
}

func (model captureModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	switch typed := message.(type) {
	case tea.KeyPressMsg:
		if typed.String() == "ctrl+c" || typed.String() == "q" {
			return model, tea.Quit
		}
	case tea.WindowSizeMsg:
		model.viewport.SetWidth(max(20, typed.Width))
		model.viewport.SetHeight(max(3, typed.Height-5))
	case captureTick:
		data, err := os.ReadFile(model.capturePath)
		if err == nil {
			content := string(data)
			followBottom := model.viewport.AtBottom()
			model.viewport.SetContent(content)
			if followBottom {
				model.viewport.GotoBottom()
			}
			model.done = strings.Contains(content, "Capture completed:") || strings.Contains(content, `"type":"completed"`)
			model.failed = strings.Contains(content, "Capture failed:") || strings.Contains(content, `"type":"failed"`) ||
				strings.Contains(content, "Capture timed out:") || strings.Contains(content, `"type":"timeout"`)
			if model.completion.IsZero() {
				model.completion = captureCompletion(content)
			}
		}
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
		identity = fmt.Sprintf(" · PID %d", model.pid)
	}
	status := fmt.Sprintf("%s%s · remaining %s · ↑/↓ scroll · q/Ctrl-C closes the viewer", state, identity, remaining)
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
