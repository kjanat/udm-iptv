package atomicfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func Copy(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer closeIgnoringError(input)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".udm-iptv-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer removeIgnoringError(name)
	if _, err := io.Copy(temporary, input); err != nil {
		closeIgnoringError(temporary)

		return err
	}
	if err := temporary.Chmod(0o755); err != nil {
		closeIgnoringError(temporary)

		return err
	}
	if err := temporary.Sync(); err != nil {
		closeIgnoringError(temporary)

		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}

	return os.Rename(name, target)
}

func Write(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
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
