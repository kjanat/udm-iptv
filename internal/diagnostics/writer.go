package diagnostics

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type syncingWriter interface {
	io.Writer
	Sync() error
}

type diagnosticWriter struct{ json, text syncingWriter }

func (writer diagnosticWriter) write(event Event) error {
	if writer.json != nil {
		err := json.NewEncoder(writer.json).Encode(event)
		if err != nil {
			return fmt.Errorf("encode JSON diagnostic event: %w", err)
		}
	}
	if writer.text != nil {
		if _, err := io.WriteString(writer.text, RenderEvent(event)); err != nil {
			return fmt.Errorf("write text diagnostic event: %w", err)
		}
	}
	// Logs are a bounded final batch. The terminal record (or deferred flush)
	// commits them together; snapshots remain immediately available to followers.
	if event.Type == EventLog {
		return nil
	}

	return writer.flush()
}

func (writer diagnosticWriter) flush() error {
	var result error
	for _, file := range []syncingWriter{writer.json, writer.text} {
		if file != nil {
			result = errors.Join(result, file.Sync())
		}
	}

	return result
}
