package installer

import (
	"fmt"
	"io"
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
