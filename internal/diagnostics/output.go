package diagnostics

import (
	"fmt"
	"io"
)

func writef(writer io.Writer, format string, arguments ...any) error {
	if _, err := fmt.Fprintf(writer, format, arguments...); err != nil {
		return fmt.Errorf("write diagnostics output: %w", err)
	}

	return nil
}

func writeString(writer io.Writer, value string) error {
	if _, err := io.WriteString(writer, value); err != nil {
		return fmt.Errorf("write diagnostics output: %w", err)
	}

	return nil
}

func closeIgnoringError(closer io.Closer) {
	_ = closer.Close()
}

func fallbackText(value string) string {
	if value == "" {
		return valueUnknown
	}

	return value
}

// mldText names the MLD version the proxy runs, or says IPv6 multicast is
// left alone.
func mldText(version int) string {
	if version == 0 {
		return "disabled"
	}

	return fmt.Sprintf("v%d", version)
}
