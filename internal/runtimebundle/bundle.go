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
)

// Preserve snapshots trusted system files without executing the proxy or ldd.
// Content-addressed generations retain the prior working runtime on failure.
func Preserve(stateDir, program, source string) error {
	if program != "improxy" && program != "igmpproxy" {
		return errors.New("unsupported proxy")
	}
	files, err := dependencies(source)
	if err != nil {
		return err
	}
	root := filepath.Join(stateDir, "runtime")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(root, ".bundle-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	hash := sha256.New()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var total int64
	for _, name := range names {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(stage, name)), 0o700); err != nil {
			return err
		}
		input, err := os.Open(files[name])
		if err != nil {
			return err
		}
		info, err := input.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 32<<20 || total+info.Size() > 64<<20 {
			_ = input.Close()

			return fmt.Errorf("invalid or oversized runtime file: %s", name)
		}
		total += info.Size()
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00", name, info.Size())
		output, err := os.OpenFile(filepath.Join(stage, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
		if err != nil {
			_ = input.Close()

			return err
		}
		n, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, info.Size()+1))
		err = errors.Join(copyErr, input.Close(), output.Sync(), output.Close())
		if err != nil {
			return err
		}
		if n != info.Size() {
			return errors.New("runtime changed during backup; retry installation")
		}
	}
	generation := hex.EncodeToString(hash.Sum(nil))
	target := filepath.Join(root, generation)
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		err := os.Rename(stage, target)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	linkDir, err := os.MkdirTemp(root, ".link-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(linkDir) }()
	link := filepath.Join(linkDir, program)
	if err := os.Symlink(generation, link); err != nil {
		return err
	}

	return os.Rename(link, filepath.Join(root, program))
}

// Command returns a private runtime invocation when firmware erased the proxy.
func Command(stateDir, program string, args []string) (string, []string, error) {
	if program != "improxy" && program != "igmpproxy" {
		return "", nil, errors.New("unsupported proxy")
	}
	root, err := filepath.EvalSymlinks(filepath.Join(stateDir, "runtime", program))
	if err != nil {
		return "", nil, fmt.Errorf("offline proxy unavailable; reinstall udm-iptv: %w", err)
	}
	binary := filepath.Join(root, "program")
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		return "", nil, errors.New("offline proxy is incomplete; reinstall udm-iptv")
	}
	loader := filepath.Join(root, "loader")
	if _, err := os.Stat(loader); err == nil {
		return loader, append([]string{"--library-path", filepath.Join(root, "lib"), binary}, args...), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}

	return binary, args, nil
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
		if len(seen) > 128 {
			return nil, errors.New("too many runtime dependencies")
		}
		file, err := elf.Open(current)
		if err != nil {
			return nil, fmt.Errorf("read proxy ELF: %w", err)
		}
		libraries, err := file.ImportedLibraries()
		if err != nil {
			_ = file.Close()

			return nil, err
		}
		for _, segment := range file.Progs {
			if segment.Type != elf.PT_INTERP {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(segment.Open(), 4096))
			if err != nil {
				_ = file.Close()

				return nil, err
			}
			loader := strings.TrimRight(string(data), "\x00")
			if !filepath.IsAbs(loader) {
				_ = file.Close()

				return nil, errors.New("invalid ELF interpreter")
			}
			files["loader"] = loader
			queue = append(queue, loader)
		}
		paths, _ := file.DynString(elf.DT_RUNPATH)
		if len(paths) == 0 {
			paths, _ = file.DynString(elf.DT_RPATH)
		}
		var dirs []string
		for _, list := range paths {
			for _, dir := range filepath.SplitList(list) {
				dir = strings.ReplaceAll(strings.ReplaceAll(dir, "${ORIGIN}", filepath.Dir(current)), "$ORIGIN", filepath.Dir(current))
				if filepath.IsAbs(dir) {
					dirs = append(dirs, dir)
				}
			}
		}
		for _, arch := range []string{"aarch64-linux-gnu", "x86_64-linux-gnu", "arm-linux-gnueabihf"} {
			dirs = append(dirs, "/lib/"+arch, "/usr/lib/"+arch)
		}
		dirs = append(dirs, "/lib64", "/usr/lib64", "/lib", "/usr/lib")
		machine, class := file.Machine, file.Class
		_ = file.Close()
		for _, name := range libraries {
			if filepath.Base(name) != name {
				return nil, errors.New("nonportable ELF dependency")
			}
			if _, exists := files["lib/"+name]; exists {
				continue
			}
			found := ""
			for _, dir := range dirs {
				candidate := filepath.Join(dir, name)
				library, err := elf.Open(candidate)
				if err != nil {
					continue
				}
				matches := library.Machine == machine && library.Class == class
				_ = library.Close()
				if matches {
					found = candidate

					break
				}
			}
			if found == "" {
				return nil, fmt.Errorf("cannot preserve dependency %s", name)
			}
			files["lib/"+name] = found
			queue = append(queue, found)
		}
	}

	return files, nil
}
