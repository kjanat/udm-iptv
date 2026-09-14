package app

import (
	"fmt"
	"io"
	"os"
)

func writef(writer io.Writer, format string, arguments ...any) error {
	_, err := fmt.Fprintf(writer, format, arguments...)
	return err
}

func writeString(writer io.Writer, value string) error {
	_, err := io.WriteString(writer, value)
	return err
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
