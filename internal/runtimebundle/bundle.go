// Package runtimebundle preserves the proxy and its ELF dependencies offline.
package runtimebundle

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

const (
	// maxDependencies bounds the ELF dependency walk against a dependency cycle.
	maxDependencies = 128
	// interpPathLimit bounds the PT_INTERP segment read; loader paths are short.
	interpPathLimit = 4096
	// maxRuntimeFileSize bounds a single preserved file.
	maxRuntimeFileSize = 32 << 20
	// maxRuntimeTotalSize bounds the whole preserved bundle.
	maxRuntimeTotalSize = 64 << 20
)

var (
	errUnsupportedProxy       = errors.New("unsupported proxy")
	errIncompleteOfflineProxy = errors.New("offline proxy is incomplete; reinstall udm-iptv")
	errInvalidRuntimeFile     = errors.New("invalid or oversized runtime file")
	errRuntimeChanged         = errors.New("runtime changed during backup; retry installation")
	errTooManyDependencies    = errors.New("too many runtime dependencies")
	errInvalidInterpreter     = errors.New("invalid ELF interpreter")
	errNonportableDependency  = errors.New("nonportable ELF dependency")
	errDependencyNotFound     = errors.New("cannot preserve dependency")
)

// Preserve snapshots trusted system files without executing the proxy or ldd.
// Content-addressed generations retain the prior working runtime on failure.
func Preserve(stateDir, program, source string) error {
	if !supportedProxy(program) {
		return errUnsupportedProxy
	}
	files, err := dependencies(source)
	if err != nil {
		return err
	}
	root := filepath.Join(stateDir, "runtime")
	if err := os.MkdirAll(root, filemode.PrivateDir); err != nil {
		return fmt.Errorf("create runtime directory: %w", err)
	}
	stage, err := os.MkdirTemp(root, ".bundle-")
	if err != nil {
		return fmt.Errorf("create runtime staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	generation, err := stageRuntime(stage, files)
	if err != nil {
		return err
	}
	target := filepath.Join(root, generation)
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(stage, target); err != nil {
			return fmt.Errorf("install runtime generation: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect runtime generation: %w", err)
	}

	return publishGeneration(root, program, generation)
}

// Command returns a private runtime invocation when firmware erased the proxy.
func Command(stateDir, program string, args []string) (string, []string, error) {
	if !supportedProxy(program) {
		return "", nil, errUnsupportedProxy
	}
	root, err := filepath.EvalSymlinks(filepath.Join(stateDir, "runtime", program))
	if err != nil {
		return "", nil, fmt.Errorf("offline proxy unavailable; reinstall udm-iptv: %w", err)
	}
	binary := filepath.Join(root, "program")
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		return "", nil, errIncompleteOfflineProxy
	}
	loader := filepath.Join(root, "loader")
	if _, err := os.Stat(loader); err == nil {
		return loader, append([]string{"--library-path", filepath.Join(root, "lib"), binary}, args...), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("inspect offline loader: %w", err)
	}

	return binary, args, nil
}

func supportedProxy(program string) bool {
	return program == config.ProxyImproxy || program == config.ProxyIgmpproxy
}

// stageRuntime copies files into stage and returns the generation name
// addressing their names, sizes and contents.
func stageRuntime(stage string, files map[string]string) (string, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	var total int64
	for _, name := range names {
		size, err := stageRuntimeFile(stage, name, files[name], hash, total)
		if err != nil {
			return "", err
		}
		total += size
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func stageRuntimeFile(stage, name, source string, hash io.Writer, staged int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(stage, name)), filemode.PrivateDir); err != nil {
		return 0, fmt.Errorf("create runtime file directory for %s: %w", name, err)
	}
	input, err := os.Open(source)
	if err != nil {
		return 0, fmt.Errorf("open runtime file %s: %w", source, err)
	}
	info, err := input.Stat()
	if err != nil || !preservableFile(info, staged) {
		_ = input.Close()

		return 0, fmt.Errorf("%w: %s", errInvalidRuntimeFile, name)
	}
	_, _ = fmt.Fprintf(hash, "%s\x00%d\x00", name, info.Size())
	output, err := os.OpenFile(filepath.Join(stage, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, filemode.PrivateExecutable)
	if err != nil {
		_ = input.Close()

		return 0, fmt.Errorf("create staged runtime file %s: %w", name, err)
	}
	copied, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, info.Size()+1))
	if err := errors.Join(copyErr, input.Close(), output.Sync(), output.Close()); err != nil {
		return 0, err
	}
	if copied != info.Size() {
		return 0, errRuntimeChanged
	}

	return info.Size(), nil
}

func preservableFile(info os.FileInfo, staged int64) bool {
	return info.Mode().IsRegular() && info.Size() <= maxRuntimeFileSize && staged+info.Size() <= maxRuntimeTotalSize
}

func publishGeneration(root, program, generation string) error {
	linkDir, err := os.MkdirTemp(root, ".link-")
	if err != nil {
		return fmt.Errorf("create runtime link directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(linkDir) }()
	link := filepath.Join(linkDir, program)
	if err := os.Symlink(generation, link); err != nil {
		return fmt.Errorf("link runtime generation: %w", err)
	}
	if err := os.Rename(link, filepath.Join(root, program)); err != nil {
		return fmt.Errorf("publish runtime link for %s: %w", program, err)
	}

	return pruneGenerations(root)
}

// pruneGenerations removes generations no proxy link points at. A firmware
// update changes the proxy or a shared library, and each generation holds up
// to maxRuntimeTotalSize of the persistent partition.
func pruneGenerations(root string) error {
	keep := map[string]bool{}
	for _, program := range []string{config.ProxyImproxy, config.ProxyIgmpproxy} {
		target, err := os.Readlink(filepath.Join(root, program))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("read runtime link for %s: %w", program, err)
			}

			continue
		}
		keep[filepath.Base(target)] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("list runtime generations: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || keep[name] || strings.HasPrefix(name, ".") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return fmt.Errorf("remove obsolete runtime generation %s: %w", name, err)
		}
	}

	return nil
}

func dependencies(source string) (map[string]string, error) {
	files := map[string]string{"program": source}
	queue := []string{source}
	seen := map[string]bool{}
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		if seen[current] {
			continue
		}
		seen[current] = true
		if len(seen) > maxDependencies {
			return nil, errTooManyDependencies
		}
		image, err := inspectELF(current)
		if err != nil {
			return nil, err
		}
		for _, loader := range image.interpreters {
			files["loader"] = loader
			queue = append(queue, loader)
		}
		discovered, err := resolveLibraries(image, files)
		if err != nil {
			return nil, err
		}
		queue = append(queue, discovered...)
	}

	return files, nil
}

type elfImage struct {
	interpreters []string
	libraries    []string
	directories  []string
	machine      elf.Machine
	class        elf.Class
}

func inspectELF(path string) (elfImage, error) {
	file, err := elf.Open(path)
	if err != nil {
		return elfImage{}, fmt.Errorf("read proxy ELF: %w", err)
	}
	defer func() { _ = file.Close() }()
	libraries, err := file.ImportedLibraries()
	if err != nil {
		return elfImage{}, fmt.Errorf("read ELF dependencies of %s: %w", path, err)
	}
	interpreters, err := interpreterPaths(file)
	if err != nil {
		return elfImage{}, err
	}

	return elfImage{
		interpreters: interpreters,
		libraries:    libraries,
		directories:  searchDirectories(file, filepath.Dir(path)),
		machine:      file.Machine,
		class:        file.Class,
	}, nil
}

func interpreterPaths(file *elf.File) ([]string, error) {
	var paths []string
	for _, segment := range file.Progs {
		if segment.Type != elf.PT_INTERP {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(segment.Open(), interpPathLimit))
		if err != nil {
			return nil, fmt.Errorf("read ELF interpreter: %w", err)
		}
		loader := strings.TrimRight(string(data), "\x00")
		if !filepath.IsAbs(loader) {
			return nil, errInvalidInterpreter
		}
		paths = append(paths, loader)
	}

	return paths, nil
}

func searchDirectories(file *elf.File, origin string) []string {
	paths, _ := file.DynString(elf.DT_RUNPATH)
	if len(paths) == 0 {
		paths, _ = file.DynString(elf.DT_RPATH)
	}
	var dirs []string
	for _, list := range paths {
		for _, dir := range filepath.SplitList(list) {
			dir = strings.ReplaceAll(strings.ReplaceAll(dir, "${ORIGIN}", origin), "$ORIGIN", origin)
			if filepath.IsAbs(dir) {
				dirs = append(dirs, dir)
			}
		}
	}
	for _, arch := range []string{"aarch64-linux-gnu", "x86_64-linux-gnu", "arm-linux-gnueabihf"} {
		dirs = append(dirs, "/lib/"+arch, "/usr/lib/"+arch)
	}

	return append(dirs, "/lib64", "/usr/lib64", "/lib", "/usr/lib")
}

func resolveLibraries(image elfImage, files map[string]string) ([]string, error) {
	var discovered []string
	for _, name := range image.libraries {
		if filepath.Base(name) != name {
			return nil, errNonportableDependency
		}
		if _, exists := files["lib/"+name]; exists {
			continue
		}
		found, err := findLibrary(name, image)
		if err != nil {
			return nil, err
		}
		files["lib/"+name] = found
		discovered = append(discovered, found)
	}

	return discovered, nil
}

func findLibrary(name string, image elfImage) (string, error) {
	for _, dir := range image.directories {
		candidate := filepath.Join(dir, name)
		library, err := elf.Open(candidate)
		if err != nil {
			continue
		}
		matches := library.Machine == image.machine && library.Class == image.class
		_ = library.Close()
		if matches {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("%w %s", errDependencyNotFound, name)
}
