package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var errStateDirectoryMissing = errors.New("state directory does not exist")

func validateStatePath(directory string) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return fmt.Errorf("state directory must be a clean absolute path: %q", directory)
	}
	switch directory {
	case "/", "/data", "/etc", "/usr", "/usr/local", "/usr/bin", "/usr/sbin", "/usr/lib",
		"/var", "/var/lib", "/var/tmp", "/var/cache", "/tmp", "/home", "/root", "/run",
		"/opt", "/srv", "/mnt", "/media", "/boot", "/proc", "/sys", "/dev", "/bin", "/sbin", "/lib", "/lib64":
		return fmt.Errorf("state directory cannot be a shared system directory: %s", directory)
	}
	if filepath.Dir(directory) == "/home" {
		return fmt.Errorf("state directory cannot be a home directory: %s", directory)
	}
	return nil
}

// Pin the directory before stopping services; reject redirected installation roots.
func openStateDirectory(directory string) (*os.Root, error) {
	if err := validateStatePath(directory); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot("/")
	if err != nil {
		return nil, fmt.Errorf("open state directory: %w", err)
	}
	for part := range strings.SplitSeq(strings.TrimPrefix(directory, "/"), "/") {
		next, err := openStateChild(root, part)
		closeErr := root.Close()
		if err != nil || closeErr != nil {
			if next != nil {
				closeErr = errors.Join(closeErr, next.Close())
			}
			if errors.Is(err, os.ErrNotExist) && closeErr == nil {
				return nil, errStateDirectoryMissing
			}
			return nil, fmt.Errorf("open state directory %s: %w", directory, errors.Join(err, closeErr))
		}
		root = next
	}
	return root, nil
}

func openStateChild(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("state path component is not a directory: %s", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := child.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.Join(fmt.Errorf("state path changed while opening %s", name), err, child.Close())
	}
	return child, nil
}

// Remove only owned entries. Unknown files and external configurations survive.
func removeStateFiles(root *os.Root, configPath string, keepConfig bool) error {
	if root == nil {
		return nil
	}
	files := []string{"bin/udm-iptv", "bin/udhcpc-hook"}
	directories := []string{"diagnostics"}
	if !keepConfig {
		directories = append(directories, "runtime")
		files = append(files, "config.json", "legacy.conf", "udm-iptv.conf", "udm-iptv.deb", "udm-iptv-restore", "debconf.preseed", "telemetry-research.json", "telemetry-research.lock")
		for _, kind := range []string{"errors", "logs", "metrics", "traces", "presets", "network"} {
			files = append(files, "telemetry-"+kind+".rate")
		}
		if relative, err := filepath.Rel(root.Name(), configPath); err == nil && relative != "." && filepath.IsLocal(relative) {
			files = append(files, relative)
		}
	}
	for _, name := range files {
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect installation file %s: %w", name, err)
		}
		if info.IsDir() {
			return fmt.Errorf("installation file is unexpectedly a directory: %s", name)
		}
		if err := root.Remove(name); err != nil {
			return fmt.Errorf("remove installation file %s: %w", name, err)
		}
	}
	// Recovery copies have unique names; remove no unrelated bin entries.
	bin, err := root.Open("bin")
	if err == nil {
		entries, readErr := bin.ReadDir(-1)
		err = errors.Join(readErr, bin.Close())
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() && (entry.Name() == ".udm-iptv.previous" || strings.HasPrefix(entry.Name(), ".udm-iptv.previous-")) {
				if err := root.Remove("bin/" + entry.Name()); err != nil {
					return err
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, name := range directories {
		if err := root.RemoveAll(name); err != nil {
			return fmt.Errorf("remove installation directory %s: %w", name, err)
		}
	}
	return removeEmpty(root, "bin")
}

func removeEmpty(root *os.Root, name string) error {
	err := root.Remove(name)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
		return nil
	}
	return err
}
