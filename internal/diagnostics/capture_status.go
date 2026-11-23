package diagnostics

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

// StatusPath names the private acknowledgement beside either capture format.
// It holds only the latest lifecycle event, so launchers need not scan a capture.
func StatusPath(options Options) string {
	path := options.JSONPath
	if path == "" {
		path = options.TextPath
	}
	if path == "" {
		return ""
	}
	return strings.TrimSuffix(path, filepath.Ext(path)) + ".status.json"
}

func writeCaptureStatus(path string, event Event) error {
	if path == "" {
		return nil
	}
	switch event.Type {
	case EventStarted, EventCompleted, EventTimeout, EventFailed:
	default:
		return nil
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode capture status: %w", err)
	}
	if err := atomicfile.Write(path, data, filemode.PrivateFile); err != nil {
		return fmt.Errorf("write capture status: %w", err)
	}
	return nil
}
