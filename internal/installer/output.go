package installer

import (
	"fmt"
	"io"
	"os"
)

func writef(writer io.Writer, format string, arguments ...any) error {
	if _, err := fmt.Fprintf(writer, format, arguments...); err != nil {
		return fmt.Errorf("write installer progress output: %w", err)
	}

	return nil
}

func writeString(writer io.Writer, value string) error {
	if _, err := io.WriteString(writer, value); err != nil {
		return fmt.Errorf("write installer progress output: %w", err)
	}

	return nil
}

func closeIgnoringError(closer io.Closer) {
	_ = closer.Close()
}

func removeIgnoringError(path string) {
	_ = os.Remove(path)
}

func removeAllIgnoringError(path string) {
	_ = os.RemoveAll(path)
}
