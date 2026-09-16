package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/diagnostics"
	"github.com/kjanat/udm-iptv/internal/ui"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
)

func (application *Application) diagnoseCommand() *cobra.Command {
	options := diagnostics.Options{Format: "text", Verbosity: "normal"}
	command := &cobra.Command{
		Use: "diagnose", Short: "Collect privacy-conscious IPTV diagnostics", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if options.FollowFile != "" {
				_, err := tea.NewProgram(ui.NewCaptureModel(options.FollowFile, time.Time{}, 0)).Run()

				return err
			}
			if options.Capture == 0 {
				value, err := application.collector().Snapshot(command.Context())
				if err != nil {
					return err
				}
				if options.Format == "jsonl" {
					return json.NewEncoder(application.Out).Encode(diagnostics.Event{Time: value.Timestamp, Type: "snapshot", Snapshot: &value})
				}

				return writeString(application.Out, diagnostics.RenderSnapshot(value))
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
	_ = command.RegisterFlagCompletionFunc("format", completeValues("text\tshare-ready report", "jsonl\tstructured events", "both\tcapture formats"))
	_ = command.RegisterFlagCompletionFunc("verbosity", completeValues("summary\tsample every 2 minutes", "normal\tsample every 15 seconds", "debug\tsample every 5 seconds"))

	return command
}

func (application *Application) startCapture(ctx context.Context, options diagnostics.Options) error {
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
		err := atomicfile.Write(path, nil, 0o600)
		if err != nil {
			return err
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	// The worker intentionally outlives this command and its SSH session.
	worker := exec.Command(executable, "diagnose-worker", //nolint:noctx // Detached capture owns its deadline.
		"--config", application.ConfigPath,
		"--duration", options.Capture.String(),
		"--verbosity", options.Verbosity,
		"--format", options.Format,
		"--text", options.TextPath,
		"--jsonl", options.JSONPath,
	)
	worker.Stdin = nil
	errorLog, err := os.CreateTemp(filepath.Dir(paths[0]), "capture-errors-*.log")
	if err != nil {
		return fmt.Errorf("create capture error log: %w", err)
	}
	defer closeIgnoringError(errorLog)
	worker.Stdout, worker.Stderr = io.Discard, errorLog
	worker.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := worker.Start(); err != nil {
		return errors.Join(fmt.Errorf("start capture worker: %w", err), os.Remove(errorLog.Name()))
	}
	if err := worker.Process.Release(); err != nil {
		return err
	}
	completion := time.Now().Add(options.Capture).UTC()
	if err := writef(application.Out, "Diagnostics capture started (PID %d).\n", worker.Process.Pid); err != nil {
		return err
	}
	if err := writef(application.Out, "Local worker errors: %s\n", errorLog.Name()); err != nil {
		return err
	}
	if err := writef(application.Out, "Expected completion: %s (%s from now).\n", completion.Format(time.RFC3339), options.Capture.Round(time.Second)); err != nil {
		return err
	}
	if options.Format == "text" || options.Format == "both" {
		err := writef(application.Out, "Share-ready text: %s\n", options.TextPath)
		if err != nil {
			return err
		}
	}
	if options.Format == "jsonl" || options.Format == "both" {
		err := writef(application.Out, "Structured JSON Lines: %s\n", options.JSONPath)
		if err != nil {
			return err
		}
	}
	if options.Follow {
		followPath := options.TextPath
		if followPath == "" {
			followPath = options.JSONPath
		}
		model := ui.NewCaptureModel(followPath, completion, worker.Process.Pid)
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
	flags.StringVar(&options.Format, "format", "text", "capture format")
	flags.StringVar(&options.TextPath, "text", "", "text output")
	flags.StringVar(&options.JSONPath, "jsonl", "", "JSON Lines output")
	_ = command.MarkFlagRequired("duration")

	return command
}
