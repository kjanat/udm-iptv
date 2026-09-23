package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var (
	errStateDirectoryMissing = errors.New("state directory does not exist")
	errStatePathUnclean      = errors.New("state directory must be a clean absolute path")
	errStateDirectoryShared  = errors.New("state directory cannot be a shared system directory")
	errStateDirectoryHome    = errors.New("state directory cannot be a home directory")
	errStatePathNotDirectory = errors.New("state path component is not a directory")
	errStatePathChanged      = errors.New("state path changed while opening")
	errStateFileIsDirectory  = errors.New("installation file is unexpectedly a directory")
)

func validateStatePath(directory string) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return fmt.Errorf("%w: %q", errStatePathUnclean, directory)
	}
	switch directory {
	case "/", "/data", "/etc", "/usr", "/usr/local", "/usr/bin", "/usr/sbin", "/usr/lib",
		"/var", "/var/lib", "/var/tmp", "/var/cache", "/tmp", "/home", "/root", "/run",
		"/opt", "/srv", "/mnt", "/media", "/boot", "/proc", "/sys", "/dev", "/bin", "/sbin", "/lib", "/lib64":
		return fmt.Errorf("%w: %s", errStateDirectoryShared, directory)
	}
	if filepath.Dir(directory) == "/home" {
		return fmt.Errorf("%w: %s", errStateDirectoryHome, directory)
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
		return nil, fmt.Errorf("inspect state path component %s: %w", name, err)
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("%w: %s", errStatePathNotDirectory, name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("open state path component %s: %w", name, err)
	}
	after, err := child.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.Join(fmt.Errorf("%w %s", errStatePathChanged, name), err, child.Close())
	}
	return child, nil
}

// Remove only owned entries. Unknown files and external configurations survive.
func removeStateFiles(root *os.Root, configPath string, options UninstallOptions) error {
	if root == nil || options.KeepData {
		return nil
	}
	for _, name := range ownedStateFiles(root, configPath, options) {
		if err := removeOwnedFile(root, name); err != nil {
			return err
		}
	}
	if err := removeRecoveryCopies(root); err != nil {
		return err
	}
	for _, name := range ownedStateDirectories(options.KeepConfig) {
		if err := root.RemoveAll(name); err != nil {
			return fmt.Errorf("remove installation directory %s: %w", name, err)
		}
	}
	return removeEmpty(root, "bin")
}

func ownedStateFiles(root *os.Root, configPath string, options UninstallOptions) []string {
	// The lock inode survives removal, including after its holder releases it:
	// another process may already have opened that inode before taking flock.
	files := []string{"bin/udhcpc-hook"}
	if !options.FromPackage {
		files = append(files, "bin/udm-iptv")
	}
	if options.KeepConfig {
		return files
	}
	files = append(files, "config.json", "config.json.rejected", "legacy.conf", legacyNetworkPending, "udm-iptv.conf", "udm-iptv.deb", "udm-iptv-restore", "debconf.preseed", "telemetry-research.json", "telemetry-research.lock")
	for _, kind := range []string{"errors", "logs", "metrics", "traces", "presets", "network"} {
		files = append(files, "telemetry-"+kind+".rate")
	}
	if relative, err := filepath.Rel(root.Name(), configPath); err == nil && relative != "." && relative != lockName && filepath.IsLocal(relative) && (!options.FromPackage || relative != "bin/udm-iptv") {
		files = append(files, relative)
	}
	return files
}

func ownedStateDirectories(keepConfig bool) []string {
	if keepConfig {
		return []string{"diagnostics", "sigstore"}
	}
	return []string{"diagnostics", "runtime", "sigstore"}
}

func removeOwnedFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect installation file %s: %w", name, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%w: %s", errStateFileIsDirectory, name)
	}
	if err := root.Remove(name); err != nil {
		return fmt.Errorf("remove installation file %s: %w", name, err)
	}
	return nil
}

// Recovery copies have unique names; remove no unrelated bin entries.
func removeRecoveryCopies(root *os.Root) error {
	bin, err := root.Open("bin")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("open installed bin directory: %w", err)
	}
	entries, readErr := bin.ReadDir(-1)
	if err := errors.Join(readErr, bin.Close()); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || (entry.Name() != ".udm-iptv.previous" && !strings.HasPrefix(entry.Name(), ".udm-iptv.previous-")) {
			continue
		}
		if err := root.Remove("bin/" + entry.Name()); err != nil {
			return fmt.Errorf("remove recovery copy bin/%s: %w", entry.Name(), err)
		}
	}

	return nil
}

func removeEmpty(root *os.Root, name string) error {
	return ignoreNonEmpty(root.Remove(name))
}

func ignoreNonEmpty(err error) error {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
		return nil
	}
	return err
}
