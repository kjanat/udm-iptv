package atomicfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kjanat/udm-iptv/internal/filemode"
)

// Copy atomically replaces target with source's content, mode preserved as executable.
func Copy(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open %s: %w", source, err)
	}
	defer closeIgnoringError(input)
	if err := os.MkdirAll(filepath.Dir(target), filemode.SharedDir); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", target, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".udm-iptv-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", target, err)
	}
	name := temporary.Name()
	defer removeIgnoringError(name)
	if _, err := io.Copy(temporary, input); err != nil {
		closeIgnoringError(temporary)

		return fmt.Errorf("copy %s to %s: %w", source, target, err)
	}
	if err := temporary.Chmod(filemode.Executable); err != nil {
		closeIgnoringError(temporary)

		return fmt.Errorf("set file permissions for %s: %w", target, err)
	}
	if err := temporary.Sync(); err != nil {
		closeIgnoringError(temporary)

		return fmt.Errorf("sync %s: %w", target, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", target, err)
	}
	if err := os.Rename(name, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}

	return nil
}

// Write atomically replaces path with data, creating parent directories as needed.
func Write(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), filemode.SharedDir); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", path, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".udm-iptv-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	name := temporary.Name()
	defer removeIgnoringError(name)
	if err := temporary.Chmod(mode); err != nil {
		closeIgnoringError(temporary)

		return fmt.Errorf("set file permissions for %s: %w", path, err)
	}
	if _, err := temporary.Write(data); err != nil {
		closeIgnoringError(temporary)

		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		closeIgnoringError(temporary)
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func closeIgnoringError(closer io.Closer) {
	_ = closer.Close()
}

func removeIgnoringError(path string) {
	_ = os.Remove(path)
}
