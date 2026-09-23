package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/diagnostics"
	"github.com/kjanat/udm-iptv/internal/filemode"
	"github.com/kjanat/udm-iptv/internal/ui"
)

const (
	formatText  = "text"
	formatJSON  = "json"
	formatJSONL = "jsonl"
	formatBoth  = "both"
)

const (
	minCaptureWindow = time.Second
	maxCaptureWindow = 24 * time.Hour
)

var (
	captureFormats     = []string{formatText, formatJSONL, formatBoth}
	captureVerbosities = []string{"summary", "normal", flagDebug}
)

var (
	errCaptureWindowRange     = errors.New("capture duration must be between 1s and 24h")
	errCaptureWindowTooShort  = errors.New("capture duration must be at least 1s")
	errUnknownFormat          = errors.New("unknown format")
	errUnknownVerbosity       = errors.New("unknown verbosity")
	errFollowNeedsCapture     = errors.New("--follow requires --capture")
	errBothFormatsNeedCapture = errors.New("--format both requires --capture")
	errFollowFileStandsAlone  = errors.New("--follow-file cannot be combined with --capture or --follow")
	errCapturePathNotRegular  = errors.New("capture path must be a regular file")
	errCaptureWorkerExited    = errors.New("the capture worker exited during initialisation")
	errCaptureNotConfirmed    = errors.New("the capture worker has not written its first record")
	errCaptureNotCompleted    = errors.New("the capture worker exited without completing its capture")
	errCaptureTerminal        = errors.New("the capture ended unsuccessfully")
)

// diagnoseRule reports why an invocation of diagnose is not runnable.
type diagnoseRule func(*cobra.Command, diagnostics.Options) error

var diagnoseRules = []diagnoseRule{
	captureWindowInRange,
	formatIsKnown,
	verbosityIsKnown,
	followNeedsCapture,
	bothFormatsNeedCapture,
	followFileStandsAlone,
	followFileIsRegular,
}

func captureWindowInRange(_ *cobra.Command, options diagnostics.Options) error {
	if options.Capture < 0 || options.Capture > maxCaptureWindow {
		return errCaptureWindowRange
	}
	if options.Capture > 0 && options.Capture < minCaptureWindow {
		return errCaptureWindowTooShort
	}

	return nil
}

func formatIsKnown(_ *cobra.Command, options diagnostics.Options) error {
	if !slices.Contains(captureFormats, options.Format) {
		return fmt.Errorf("%w %q", errUnknownFormat, options.Format)
	}

	return nil
}

func verbosityIsKnown(_ *cobra.Command, options diagnostics.Options) error {
	if !slices.Contains(captureVerbosities, options.Verbosity) {
		return fmt.Errorf("%w %q", errUnknownVerbosity, options.Verbosity)
	}

	return nil
}

func followNeedsCapture(_ *cobra.Command, options diagnostics.Options) error {
	if options.Follow && options.Capture == 0 {
		return errFollowNeedsCapture
	}

	return nil
}

func bothFormatsNeedCapture(_ *cobra.Command, options diagnostics.Options) error {
	if options.Capture == 0 && options.Format == formatBoth {
		return errBothFormatsNeedCapture
	}

	return nil
}

func followFileStandsAlone(command *cobra.Command, options diagnostics.Options) error {
	if options.FollowFile == "" {
		return nil
	}
	if command.Flags().Changed("capture") || options.Follow {
		return errFollowFileStandsAlone
	}

	return nil
}

func followFileIsRegular(_ *cobra.Command, options diagnostics.Options) error {
	if options.FollowFile == "" {
		return nil
	}
	info, err := os.Stat(options.FollowFile)
	if err != nil {
		return fmt.Errorf("open capture: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errCapturePathNotRegular
	}

	return nil
}

func checkDiagnoseOptions(command *cobra.Command, options diagnostics.Options) error {
	for _, rule := range diagnoseRules {
		if err := rule(command, options); err != nil {
			return err
		}
	}

	return nil
}

func (application *Application) diagnoseCommand() *cobra.Command {
	options := diagnostics.Options{Format: formatText, Verbosity: "normal"}
	command := &cobra.Command{
		Use: "diagnose", Aliases: []string{"diag"}, Short: "Collect IPTV diagnostics", Args: cobra.NoArgs,
		PreRunE: func(command *cobra.Command, _ []string) error {
			if options.Capture > 0 && !command.Flags().Changed("format") {
				options.Format = formatBoth
			}
			if options.Format == formatJSON {
				options.Format = formatJSONL
			}
			return checkDiagnoseOptions(command, options)
		},
		RunE: func(command *cobra.Command, _ []string) error {
			switch {
			case options.FollowFile != "":
				return followCapture(options.FollowFile, time.Time{}, 0)
			case options.Capture == 0:
				return application.reportSnapshot(command.Context(), options)
			default:
				return application.startCapture(command.Context(), options)
			}
		},
	}
	flags := command.Flags()
	flags.DurationVar(&options.Capture, "capture", 0, "capture a bounded timeline, for example 15m or 2h")
	flags.StringVar(&options.Format, "format", options.Format, "output format: text, jsonl (or json), or both")
	flags.StringVar(&options.Verbosity, "verbosity", options.Verbosity, "report detail or capture sample frequency: summary, normal, or debug")
	flags.BoolVar(&options.Follow, "follow", false, "follow the capture in an interactive terminal view")
	flags.StringVar(&options.FollowFile, "follow-file", "", "follow an existing text capture")
	_ = flags.MarkHidden("follow-file")
	_ = command.RegisterFlagCompletionFunc("format", completeValues("text\treadable report", "json\tstructured events", "jsonl\tstructured events", "both\tcapture formats"))
	_ = command.RegisterFlagCompletionFunc("verbosity", completeValues("summary\tsample every 2 minutes", "normal\tsample every 15 seconds", "debug\tsample every 5 seconds"))
	command.AddCommand(application.diagnoseExportCommand())

	return command
}

func (application *Application) reportSnapshot(ctx context.Context, options diagnostics.Options) error {
	value, err := application.collector().ReportSnapshot(ctx, options.Verbosity)
	if err != nil {
		return fmt.Errorf("collect diagnostics: %w", err)
	}
	if options.Format == formatJSONL {
		event := diagnostics.Event{Time: value.Timestamp, Type: "snapshot", Snapshot: &value, Privacy: diagnostics.PrivacyPrivate}
		if err := json.NewEncoder(application.Out).Encode(event); err != nil {
			return fmt.Errorf("encode snapshot: %w", err)
		}

		return nil
	}

	return writeString(application.Out, ui.StatusText(diagnostics.RenderSnapshot(value)))
}

func followCapture(path string, completion time.Time, pid int) error {
	if _, err := tea.NewProgram(ui.NewCaptureModel(path, completion, pid)).Run(); err != nil {
		return fmt.Errorf("follow capture %s: %w", path, err)
	}

	return nil
}

func wantsText(format string) bool { return format == formatText || format == formatBoth }

func wantsJSON(format string) bool { return format == formatJSONL || format == formatBoth }

// captureFollowPath prefers the structured file, which the viewer turns
// into a status area and a timeline; a text capture is shown as is.
func captureFollowPath(options diagnostics.Options) string {
	if options.JSONPath != "" {
		return options.JSONPath
	}

	return options.TextPath
}

func (application *Application) startCapture(ctx context.Context, options diagnostics.Options) error {
	if options.Follow && !wantsJSON(options.Format) {
		options.Format = formatBoth
	}
	options, directory, err := application.prepareCaptureFiles(options)
	if err != nil {
		return err
	}
	errorLog, err := os.CreateTemp(directory, "capture-errors-*.log")
	if err != nil {
		return fmt.Errorf("create capture error log: %w", err)
	}
	defer closeIgnoringError(errorLog)
	worker, err := application.startCaptureWorker(ctx, options, errorLog)
	if err != nil {
		return err
	}
	status, err := confirmCaptureStarted(worker, diagnostics.StatusPath(options), errorLog.Name(), captureStartGrace)
	if err != nil {
		return err
	}
	pid := worker.Process.Pid
	if err := application.reportCaptureStarted(options, pid, errorLog.Name(), status); err != nil {
		return err
	}
	if options.Follow {
		return followCapture(captureFollowPath(options), status.Deadline, pid)
	}

	return writef(application.Out, "Follow it with: udm-iptv diagnose --follow-file %s\n", captureFollowPath(options))
}

func (application *Application) prepareCaptureFiles(options diagnostics.Options) (diagnostics.Options, string, error) {
	directory := filepath.Join(application.StateDir, "diagnostics")
	if err := os.MkdirAll(directory, filemode.PrivateDir); err != nil {
		return options, "", fmt.Errorf("create diagnostics directory %s: %w", directory, err)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	base := filepath.Join(directory, "udm-iptv-"+stamp+"-"+strconv.Itoa(os.Getpid()))
	if wantsText(options.Format) {
		options.TextPath = base + ".txt"
	}
	if wantsJSON(options.Format) {
		options.JSONPath = base + ".jsonl"
	}
	for _, path := range []string{options.TextPath, options.JSONPath, diagnostics.StatusPath(options)} {
		if path == "" {
			continue
		}
		if err := atomicfile.Write(path, nil, filemode.PrivateFile); err != nil {
			return options, "", fmt.Errorf("create capture file %s: %w", path, err)
		}
	}

	return options, directory, nil
}

// The worker intentionally outlives this command and its SSH session.
func (application *Application) startCaptureWorker(ctx context.Context, options diagnostics.Options, errorLog *os.File) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate the running executable: %w", err)
	}
	worker := exec.CommandContext(context.WithoutCancel(ctx), executable, "diagnose-worker",
		"--config", application.ConfigPath,
		"--duration", options.Capture.String(),
		"--verbosity", options.Verbosity,
		"--format", options.Format,
		"--text", options.TextPath,
		"--jsonl", options.JSONPath,
	)
	worker.Stdin = nil
	worker.Stdout, worker.Stderr = io.Discard, errorLog
	worker.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := worker.Start(); err != nil {
		return nil, errors.Join(fmt.Errorf("start capture worker: %w", err), os.Remove(errorLog.Name()))
	}

	return worker, nil
}

const (
	// captureStartGrace bounds the wait for the worker's first record; a
	// worker writes it right after opening its outputs.
	captureStartGrace = 5 * time.Second
	captureStartPoll  = 50 * time.Millisecond
)

// confirmCaptureStarted reads the worker's atomic lifecycle acknowledgement.
// Process liveness and exit status alone cannot establish capture success.
func confirmCaptureStarted(worker *exec.Cmd, statusPath, errorLogPath string, grace time.Duration) (diagnostics.Event, error) {
	exited := make(chan error, 1)
	go func() { exited <- worker.Wait() }()
	deadline := time.After(grace)
	poll := time.NewTicker(captureStartPoll)
	defer poll.Stop()
	for {
		select {
		case cause := <-exited:
			if cause != nil {
				return diagnostics.Event{}, errors.Join(captureWorkerFailure(cause, errorLogPath), os.Remove(errorLogPath))
			}
			status, err := captureAcknowledgement(statusPath)
			if err != nil {
				return status, err
			}
			if status.Type != diagnostics.EventCompleted {
				return status, errCaptureNotCompleted
			}
			return status, nil
		case <-deadline:
			return diagnostics.Event{}, fmt.Errorf("%w within %s: PID %d, status %s, errors %s", errCaptureNotConfirmed, grace, worker.Process.Pid, statusPath, errorLogPath)
		case <-poll.C:
			status, err := captureAcknowledgement(statusPath)
			if err != nil {
				return status, err
			}
			if status.Type == diagnostics.EventStarted {
				return status, nil
			}
			// A terminal success waits for process exit, including output-close errors.
		}
	}
}

func captureAcknowledgement(path string) (diagnostics.Event, error) {
	var status diagnostics.Event
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(data) == 0) {
		return status, nil
	}
	if err != nil {
		return status, fmt.Errorf("read capture acknowledgement: %w", err)
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return status, fmt.Errorf("decode capture acknowledgement: %w", err)
	}
	if status.Type == diagnostics.EventTimeout || status.Type == diagnostics.EventFailed {
		return status, fmt.Errorf("%w (%s): %s", errCaptureTerminal, status.Type, status.Message)
	}
	return status, nil
}

func captureWorkerFailure(cause error, errorLogPath string) error {
	log, err := os.ReadFile(errorLogPath)
	if err != nil || len(bytes.TrimSpace(log)) == 0 {
		return fmt.Errorf("%w: %w", errCaptureWorkerExited, cause)
	}

	return fmt.Errorf("%w: %w: %s", errCaptureWorkerExited, cause, bytes.TrimSpace(log))
}

func (application *Application) reportCaptureStarted(options diagnostics.Options, pid int, logPath string, status diagnostics.Event) error {
	lines := []string{
		"Private local capture: contains network identities and raw logs.\n",
		fmt.Sprintf("Diagnostics capture %s (PID %d).\n", status.Type, pid),
		fmt.Sprintf("Local worker errors: %s\n", logPath),
	}
	if status.Type == diagnostics.EventStarted {
		lines = append(lines, fmt.Sprintf("Expected completion: %s.\n", status.Deadline.Format(time.RFC3339)))
	}
	if options.TextPath != "" {
		lines = append(lines, fmt.Sprintf("Text: %s\n", options.TextPath))
	}
	if options.JSONPath != "" {
		lines = append(lines, fmt.Sprintf("Structured JSON Lines: %s\n", options.JSONPath))
	}
	for _, line := range lines {
		if err := writeString(application.Out, line); err != nil {
			return err
		}
	}

	return nil
}

func (application *Application) diagnoseWorkerCommand() *cobra.Command {
	options := diagnostics.Options{}
	command := &cobra.Command{
		Use: "diagnose-worker", Hidden: true, Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			err := application.collector().Capture(command.Context(), options)

			return errors.Join(err, diagnostics.RecordFailure(options, err))
		},
	}
	flags := command.Flags()
	flags.DurationVar(&options.Capture, "duration", 0, "capture duration")
	flags.StringVar(&options.Verbosity, "verbosity", "normal", "capture verbosity")
	flags.StringVar(&options.Format, "format", formatText, "capture format")
	flags.StringVar(&options.TextPath, "text", "", "text output")
	flags.StringVar(&options.JSONPath, "jsonl", "", "JSON Lines output")
	_ = command.MarkFlagRequired("duration")

	return command
}
