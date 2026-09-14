package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"
)

type diagnosticOptions struct {
	Capture    time.Duration
	Format     string
	Verbosity  string
	Follow     bool
	TextPath   string
	JSONPath   string
	FollowFile string
}

type diagnosticEvent struct {
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	Message  string    `json:"message,omitempty"`
	Snapshot *snapshot `json:"snapshot,omitempty"`
	Log      string    `json:"log,omitempty"`
}

func (application *Application) diagnoseCommand() *cobra.Command {
	options := diagnosticOptions{Format: "text", Verbosity: "normal"}
	command := &cobra.Command{
		Use: "diagnose", Short: "Collect privacy-conscious IPTV diagnostics", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if options.FollowFile != "" {
				_, err := tea.NewProgram(newCaptureModel(options.FollowFile, time.Time{}, 0)).Run()
				return err
			}
			if options.Capture == 0 {
				value, err := application.snapshot(command.Context())
				if err != nil {
					return err
				}
				if options.Format == "jsonl" {
					return json.NewEncoder(application.Out).Encode(diagnosticEvent{Time: value.Timestamp, Type: "snapshot", Snapshot: &value})
				}
				return writeString(application.Out, renderSnapshot(value))
			}
			return application.startCapture(command.Context(), options)
		},
	}
	flags := command.Flags()
	flags.DurationVar(&options.Capture, "capture", 0, "capture a bounded timeline, for example 15m or 2h")
	flags.StringVar(&options.Format, "format", options.Format, "output format: text, jsonl, or both")
	flags.StringVar(&options.Verbosity, "verbosity", options.Verbosity, "sample frequency: summary, normal, or debug")
	flags.BoolVar(&options.Follow, "follow", false, "follow the capture in an interactive terminal view")
	flags.StringVar(&options.FollowFile, "follow-file", "", "follow an existing text capture")
	_ = flags.MarkHidden("follow-file")
	command.PreRunE = func(command *cobra.Command, _ []string) error {
		if options.Capture < 0 || options.Capture > 24*time.Hour {
			return errors.New("capture duration must be between 1s and 24h")
		}
		if options.Capture > 0 && options.Capture < time.Second {
			return errors.New("capture duration must be at least 1s")
		}
		if options.Format != "text" && options.Format != "jsonl" && options.Format != "both" {
			return fmt.Errorf("unknown format %q", options.Format)
		}
		if options.Verbosity != "summary" && options.Verbosity != "normal" && options.Verbosity != "debug" {
			return fmt.Errorf("unknown verbosity %q", options.Verbosity)
		}
		if options.Follow && options.Capture == 0 {
			return errors.New("--follow requires --capture")
		}
		if options.Capture == 0 && options.Format == "both" {
			return errors.New("--format both requires --capture")
		}
		if options.FollowFile != "" && (command.Flags().Changed("capture") || options.Follow) {
			return errors.New("--follow-file cannot be combined with --capture or --follow")
		}
		if options.FollowFile != "" {
			if info, err := os.Stat(options.FollowFile); err != nil {
				return fmt.Errorf("open capture: %w", err)
			} else if !info.Mode().IsRegular() {
				return errors.New("capture path must be a regular file")
			}
		}
		return nil
	}
	_ = command.RegisterFlagCompletionFunc("format", completeValues("text\tshare-ready report", "jsonl\tstructured events", "both\tboth capture formats"))
	_ = command.RegisterFlagCompletionFunc("verbosity", completeValues("summary\tsample every 2 minutes", "normal\tsample every 15 seconds", "debug\tsample every 5 seconds"))
	return command
}

func (application *Application) startCapture(ctx context.Context, options diagnosticOptions) error {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	directory := filepath.Join(application.StateDir, "diagnostics")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	base := filepath.Join(directory, "udm-iptv-"+stamp+"-"+strconv.Itoa(os.Getpid()))
	var paths []string
	if options.Format == "text" || options.Format == "both" {
		options.TextPath = base + ".txt"
		paths = append(paths, options.TextPath)
	}
	if options.Format == "jsonl" || options.Format == "both" {
		options.JSONPath = base + ".jsonl"
		paths = append(paths, options.JSONPath)
	}
	for _, path := range paths {
		if err := atomicWrite(path, nil, 0o600); err != nil {
			return err
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	worker := exec.Command(executable, "diagnose-worker",
		"--config", application.ConfigPath,
		"--duration", options.Capture.String(),
		"--verbosity", options.Verbosity,
		"--format", options.Format,
		"--text", options.TextPath,
		"--jsonl", options.JSONPath,
	)
	worker.Stdin = nil
	worker.Stdout, worker.Stderr = io.Discard, io.Discard
	worker.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := worker.Start(); err != nil {
		return err
	}
	if err := worker.Process.Release(); err != nil {
		return err
	}
	completion := time.Now().Add(options.Capture).UTC()
	if err := writef(application.Out, "Diagnostics capture started (PID %d).\n", worker.Process.Pid); err != nil {
		return err
	}
	if err := writef(application.Out, "Expected completion: %s (%s from now).\n", completion.Format(time.RFC3339), options.Capture.Round(time.Second)); err != nil {
		return err
	}
	if options.Format == "text" || options.Format == "both" {
		if err := writef(application.Out, "Share-ready text: %s\n", options.TextPath); err != nil {
			return err
		}
	}
	if options.Format == "jsonl" || options.Format == "both" {
		if err := writef(application.Out, "Structured JSON Lines: %s\n", options.JSONPath); err != nil {
			return err
		}
	}
	if options.Follow {
		followPath := options.TextPath
		if followPath == "" {
			followPath = options.JSONPath
		}
		model := newCaptureModel(followPath, completion, worker.Process.Pid)
		_, err := tea.NewProgram(model).Run()
		return err
	}
	followPath := options.TextPath
	if followPath == "" {
		followPath = options.JSONPath
	}
	return writef(application.Out, "Follow it with: udm-iptv diagnose --follow-file %s\n", followPath)
}

func (application *Application) diagnoseWorkerCommand() *cobra.Command {
	options := diagnosticOptions{}
	command := &cobra.Command{
		Use: "diagnose-worker", Hidden: true, Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			err := application.capture(command.Context(), options)
			if err != nil {
				message := sanitize(err.Error())
				if options.TextPath != "" {
					if file, openErr := os.OpenFile(options.TextPath, os.O_WRONLY|os.O_APPEND, 0o600); openErr == nil {
						_ = writef(file, "\nCapture failed: %s\n", message)
						closeIgnoringError(file)
					}
				}
				if options.JSONPath != "" {
					if file, openErr := os.OpenFile(options.JSONPath, os.O_WRONLY|os.O_APPEND, 0o600); openErr == nil {
						_ = json.NewEncoder(file).Encode(diagnosticEvent{Time: time.Now().UTC(), Type: "failed", Message: message})
						closeIgnoringError(file)
					}
				}
			}
			return err
		},
	}
	flags := command.Flags()
	flags.DurationVar(&options.Capture, "duration", 0, "capture duration")
	flags.StringVar(&options.Verbosity, "verbosity", "normal", "capture verbosity")
	flags.StringVar(&options.Format, "format", "text", "capture format")
	flags.StringVar(&options.TextPath, "text", "", "text output")
	flags.StringVar(&options.JSONPath, "jsonl", "", "JSON Lines output")
	_ = command.MarkFlagRequired("duration")
	return command
}

func (application *Application) capture(ctx context.Context, options diagnosticOptions) (resultErr error) {
	ctx, stop := signalContext(ctx)
	defer stop()
	var jsonFile *os.File
	var err error
	if options.JSONPath != "" {
		jsonFile, err = os.OpenFile(options.JSONPath, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, jsonFile.Close()) }()
	}
	var textFile *os.File
	if options.TextPath != "" {
		textFile, err = os.OpenFile(options.TextPath, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, textFile.Close()) }()
	}
	write := func(event diagnosticEvent) error {
		if jsonFile != nil {
			if err := json.NewEncoder(jsonFile).Encode(event); err != nil {
				return err
			}
			if err := jsonFile.Sync(); err != nil {
				return err
			}
		}
		if textFile != nil {
			if _, err := fmt.Fprint(textFile, renderEvent(event)); err != nil {
				return err
			}
			if err := textFile.Sync(); err != nil {
				return err
			}
		}
		return nil
	}
	started := time.Now().UTC()
	ends := started.Add(options.Capture)
	cursor := journalCursor(ctx)
	if err := write(diagnosticEvent{Time: started, Type: "started", Message: "Capture started; expected completion " + ends.Format(time.RFC3339)}); err != nil {
		return err
	}
	initial, err := application.snapshot(ctx)
	if err != nil {
		return err
	}
	if err := write(diagnosticEvent{Time: initial.Timestamp, Type: "initial", Snapshot: &initial}); err != nil {
		return err
	}
	interval := 15 * time.Second
	switch options.Verbosity {
	case "summary":
		interval = 2 * time.Minute
	case "debug":
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Until(ends))
	defer deadline.Stop()
	completedMessage := "Capture reached its deadline."
	loop := true
	for loop {
		select {
		case <-ctx.Done():
			completedMessage = "Capture stopped by signal."
			loop = false
		case <-deadline.C:
			loop = false
		case <-ticker.C:
			current, snapshotErr := application.snapshot(ctx)
			if snapshotErr != nil {
				_ = write(diagnosticEvent{Time: time.Now().UTC(), Type: "error", Message: sanitize(snapshotErr.Error())})
				continue
			}
			if err := write(diagnosticEvent{Time: current.Timestamp, Type: "sample", Snapshot: &current}); err != nil {
				return err
			}
		}
	}
	finalContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if final, finalErr := application.snapshot(finalContext); finalErr == nil {
		_ = write(diagnosticEvent{Time: final.Timestamp, Type: "final", Snapshot: &final})
	}
	for _, line := range journalLines(finalContext, cursor) {
		_ = write(diagnosticEvent{Time: time.Now().UTC(), Type: "log", Log: sanitize(line)})
	}
	return write(diagnosticEvent{Time: time.Now().UTC(), Type: "completed", Message: completedMessage})
}

func renderEvent(event diagnosticEvent) string {
	switch event.Type {
	case "started":
		return "Share-ready udm-iptv diagnostics. Review before posting publicly.\n" + event.Message + "\n\n"
	case "initial":
		return "=== Initial snapshot ===\n" + renderSnapshot(*event.Snapshot) + "\n"
	case "sample":
		value := event.Snapshot
		return fmt.Sprintf("[%s] service=%s/%s proxy=%s pid=%d restarts=%d routes=%d multicast=%d\n",
			event.Time.Format(time.RFC3339), value.Service.ActiveState, value.Service.SubState, value.Service.Proxy,
			value.Service.ProxyPID, value.Service.Restarts, len(value.Network.Routes), value.Multicast.Routes)
	case "final":
		return "\n=== Final snapshot ===\n" + renderSnapshot(*event.Snapshot) + "\n"
	case "log":
		return event.Log + "\n"
	case "error":
		return "capture error: " + sanitize(event.Message) + "\n"
	case "completed":
		return "\nCapture completed: " + event.Message + "\n"
	default:
		return ""
	}
}

func journalCursor(ctx context.Context) string {
	command := exec.CommandContext(ctx, "journalctl", "-n", "0", "--show-cursor", "--no-pager", "-u", "udm-iptv.service")
	output, err := command.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		if cursor, found := strings.CutPrefix(strings.TrimSpace(line), "-- cursor: "); found {
			return cursor
		}
	}
	return ""
}

func journalLines(ctx context.Context, cursor string) []string {
	if cursor == "" {
		return []string{"journal cursor unavailable; service logs were not collected"}
	}
	arguments := []string{"--no-pager", "-o", "cat", "-u", "udm-iptv.service"}
	arguments = append(arguments, "--after-cursor", cursor)
	command := exec.CommandContext(ctx, "journalctl", arguments...)
	output, err := command.Output()
	if err != nil {
		return []string{"journal unavailable: " + err.Error()}
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if len(lines) > 10_000 {
		lines = lines[len(lines)-10_000:]
	}
	return lines
}

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

func newCaptureModel(capturePath string, completion time.Time, pid int) captureModel {
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
			model.failed = strings.Contains(content, "Capture failed:") || strings.Contains(content, `"type":"failed"`)
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
	index := strings.Index(content, marker)
	if index < 0 {
		return time.Time{}
	}
	value := content[index+len(marker):]
	value, _, _ = strings.Cut(value, "\n")
	value, _, _ = strings.Cut(value, `"`)
	parsed, _ := time.Parse(time.RFC3339, strings.TrimSpace(value))
	return parsed
}

func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
}
