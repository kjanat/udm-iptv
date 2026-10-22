package diagnostics

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Preserve the labels on original captures and older sanitized exports.
const (
	PrivacyPrivate   = "private"
	PrivacySanitized = "sanitized"
)

const exportTextFormat = "text"

var (
	errExportSnapshotMissing = errors.New("capture snapshot event has no snapshot")
	errExportEventObject     = errors.New("capture event must be a JSON object")
	errExportFormat          = errors.New("export format must be text or jsonl")
)

// ExportCapture retains the original event records, including fields introduced
// by newer versions. Text includes the readable view and its complete JSON record.
// The input is never rewritten, anonymized or relabelled.
func ExportCapture(input io.Reader, output io.Writer, format string) error {
	if format != "jsonl" && format != exportTextFormat {
		return errExportFormat
	}
	decoder := json.NewDecoder(input)
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read capture event: %w", err)
		}
		event, err := exportEvent(raw)
		if err != nil {
			return err
		}
		if format == exportTextFormat {
			if _, err := io.WriteString(output, RenderEvent(event)+"\nEvent JSON: "); err != nil {
				return fmt.Errorf("write capture text: %w", err)
			}
		}
		if err := encoder.Encode(raw); err != nil {
			return fmt.Errorf("write capture export: %w", err)
		}
	}
}

func exportEvent(raw json.RawMessage) (Event, error) {
	if len(raw) == 0 || raw[0] != '{' {
		return Event{}, errExportEventObject
	}
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return Event{}, fmt.Errorf("decode capture event: %w", err)
	}
	if snapshotEvent(event.Type) && event.Snapshot == nil {
		return Event{}, errExportSnapshotMissing
	}
	return event, nil
}
