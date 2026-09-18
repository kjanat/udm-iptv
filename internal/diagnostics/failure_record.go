package diagnostics

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

var errNotRegularFile = errors.New("report must be a regular file")

// RecordFailure attempts both outputs, preserving every failure for stderr.
func RecordFailure(options Options, cause error) error {
	if cause == nil {
		return nil
	}
	message := cause.Error()
	var result error
	for _, output := range []struct {
		path string
		json bool
	}{{options.TextPath, false}, {options.JSONPath, true}} {
		if output.path == "" {
			continue
		}
		err := appendFailure(output.path, func(writer io.Writer) error {
			if output.json {
				return json.NewEncoder(writer).Encode(Event{Time: time.Now().UTC(), Type: EventFailed, Message: message})
			}
			return writef(writer, "\nCapture failed: %s\n", message)
		})
		if err != nil {
			result = errors.Join(result, fmt.Errorf("record capture failure in %s: %w", output.path, err))
		}
	}
	return result
}

func appendFailure(path string, write func(io.Writer) error) (result error) {
	file, err := openReport(path)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	if err := write(file); err != nil {
		return fmt.Errorf("write failure record: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync failure record: %w", err)
	}
	return nil
}

func openReport(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open report: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect report: %w", err), file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(errNotRegularFile, file.Close())
	}
	return file, nil
}
