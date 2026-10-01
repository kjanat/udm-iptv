package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
	"github.com/kjanat/udm-iptv/internal/service"
)

var errUnsupportedRecoveryFile = errors.New("installation recovery supports only files, directories and symlinks")

// InstallationTransaction finalizes a replacement. Its zero value represents
// an installation without a prior standalone installation to restore.
type InstallationTransaction struct {
	finish func(context.Context, error) error
}

type installationSnapshot struct {
	directory string
	paths     []recoveryPath
}

type recoveryPath struct {
	path    string
	present bool
}

var errPackageRecordAhead = errors.New("dpkg still records the new package version while the previous executable runs; fix the cause, then run udm-iptv upgrade again")

// previousExecutable is where the Debian preinst keeps the executable dpkg
// is about to replace, since postinst runs only after the new one is in place.
func previousExecutable(plan Plan) string {
	return filepath.Join(plan.StateDir, "bin", ".udm-iptv.previous")
}

// Begin snapshots a replaced installation before any stage mutates it. A
// package upgrade restores the executable from the preinst's copy; without
// that copy there is nothing to go back to.
func (backend SystemBackend) Begin(ctx context.Context, plan Plan) (InstallationTransaction, error) {
	if !plan.Replace || !Installed(plan.StateDir) {
		return InstallationTransaction{}, nil
	}
	sources, recoverable, err := recoverySources(ctx, plan)
	if err != nil || !recoverable {
		return InstallationTransaction{}, err
	}
	paths := []string{
		installedExecutable(plan), plan.ConfigPath, unitPath, tmpfilesPath, completionPath,
		commandPath, filepath.Join(plan.StateDir, "bin", "udhcpc-hook"),
		filepath.Join(plan.StateDir, "runtime"),
		"/etc/systemd/system/multi-user.target.wants/udm-iptv.service",
	}
	snapshot, err := snapshotInstallation(plan.StateDir, paths, sources)
	if err != nil {
		return InstallationTransaction{}, err
	}
	return InstallationTransaction{finish: backend.finishTransaction(snapshot, sources)}, nil
}

// finishTransaction discards the snapshot on success and restores it on
// failure, then removes the preinst's copies either way. A restored package
// upgrade leaves dpkg's record ahead of the executable, which the error says.
func (backend SystemBackend) finishTransaction(snapshot installationSnapshot, sources map[string]string) func(context.Context, error) error {
	return func(ctx context.Context, cause error) error {
		err := snapshot.finish(ctx, cause, stopReplacementService, backend.recoverInstallation)
		if cause != nil && len(sources) != 0 {
			err = errors.Join(err, errPackageRecordAhead)
		}
		for _, source := range sources {
			if removeErr := os.Remove(source); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("remove the previous executable copy %s: %w", source, removeErr))
			}
		}
		return err
	}
}

// recoverySources says where a replaced file is copied from when that is
// not its own path. A package upgrade without the preinst's copy has no
// executable to go back to.
func recoverySources(ctx context.Context, plan Plan) (map[string]string, bool, error) {
	packageRecord, err := QueryPackage(ctx)
	if err != nil {
		return nil, false, err
	}
	sources := map[string]string{}
	if !packageRecord.Owned() {
		return sources, true, nil
	}
	previous := previousExecutable(plan)
	if _, err := os.Stat(previous); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, fmt.Errorf("inspect the previous executable %s: %w", previous, err)
	}
	sources[installedExecutable(plan)] = previous

	return sources, true, nil
}

// snapshotInstallation copies each path aside. A path with an entry in
// sources is copied from there instead, and restored to the path.
func snapshotInstallation(stateDir string, paths []string, sources map[string]string) (installationSnapshot, error) {
	directory, err := os.MkdirTemp(stateDir, ".install-recovery-")
	if err != nil {
		return installationSnapshot{}, fmt.Errorf("create installation recovery directory: %w", err)
	}
	snapshot := installationSnapshot{directory: directory}
	for index, path := range paths {
		source := path
		if replacement, ok := sources[path]; ok {
			source = replacement
		}
		info, err := os.Lstat(source)
		present := !errors.Is(err, os.ErrNotExist)
		if err == nil && info.IsDir() && path != filepath.Join(stateDir, "runtime") {
			return installationSnapshot{}, errors.Join(fmt.Errorf("%w: %s", errStateFileIsDirectory, path), os.RemoveAll(directory))
		}
		if present {
			err = copyRecoveryPath(source, snapshot.entry(index))
		}
		if present && err != nil {
			return installationSnapshot{}, errors.Join(err, os.RemoveAll(directory))
		}
		snapshot.paths = append(snapshot.paths, recoveryPath{path: path, present: present})
	}
	return snapshot, nil
}

func (snapshot installationSnapshot) entry(index int) string {
	return filepath.Join(snapshot.directory, strconv.Itoa(index))
}

func (snapshot installationSnapshot) restore() error {
	for index, entry := range snapshot.paths {
		if err := os.RemoveAll(entry.path); err != nil {
			return fmt.Errorf("remove rejected installation path %s: %w", entry.path, err)
		}
		if entry.present {
			if err := copyRecoveryPath(snapshot.entry(index), entry.path); err != nil {
				return fmt.Errorf("restore installation path %s: %w", entry.path, err)
			}
		}
	}
	return nil
}

func (snapshot installationSnapshot) finish(ctx context.Context, cause error, stop, recoverService func(context.Context) error) error {
	if cause == nil {
		return snapshot.discard()
	}
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	for _, recoverStep := range []func(context.Context) error{
		stop,
		func(context.Context) error { return snapshot.restore() },
		recoverService,
	} {
		if err := recoverStep(recovery); err != nil {
			return errors.Join(cause, fmt.Errorf("installation recovery failed; backup retained at %s: %w", snapshot.directory, err))
		}
	}
	return errors.Join(fmt.Errorf("installation failed and previous installation was restored: %w", cause), snapshot.discard())
}

func (snapshot installationSnapshot) discard() error {
	if err := os.RemoveAll(snapshot.directory); err != nil {
		return fmt.Errorf("remove installation recovery directory %s: %w", snapshot.directory, err)
	}
	return nil
}

func stopReplacementService(ctx context.Context) error {
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		return fmt.Errorf("connect to systemd for installation recovery: %w", err)
	}
	defer connection.Close()
	if err := service.Stop(ctx, connection, service.Unit); err != nil && !service.NoSuchUnit(err) {
		return fmt.Errorf("stop rejected installation: %w", err)
	}
	return nil
}

func (backend SystemBackend) recoverInstallation(ctx context.Context) error {
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		return fmt.Errorf("connect to systemd for installation recovery: %w", err)
	}
	defer connection.Close()
	if err := connection.ReloadContext(ctx); err != nil {
		return fmt.Errorf("reload restored unit: %w", err)
	}
	if err := service.Restart(ctx, connection, service.Unit); err != nil {
		return fmt.Errorf("restart restored installation: %w", err)
	}
	return backend.Health(ctx)
}

func copyRecoveryPath(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect recovery source %s: %w", source, err)
	}
	switch {
	case info.Mode().IsRegular():
		return copyRecoveryFile(source, target, info.Mode().Perm())
	case info.Mode()&os.ModeSymlink != 0:
		return copyRecoveryLink(source, target)
	case info.IsDir():
		return copyRecoveryDirectory(source, target, info.Mode().Perm())
	default:
		return fmt.Errorf("%w: %s", errUnsupportedRecoveryFile, source)
	}
}

func copyRecoveryFile(source, target string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read recovery source %s: %w", source, err)
	}
	if err := atomicfile.Write(target, data, mode); err != nil {
		return fmt.Errorf("write recovery file %s: %w", target, err)
	}
	return nil
}

func copyRecoveryLink(source, target string) error {
	link, err := os.Readlink(source)
	if err != nil {
		return fmt.Errorf("read recovery link %s: %w", source, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), filemode.PrivateDir); err != nil {
		return fmt.Errorf("create recovery link directory: %w", err)
	}
	if err := os.Symlink(link, target); err != nil {
		return fmt.Errorf("write recovery link %s: %w", target, err)
	}
	return nil
}

func copyRecoveryDirectory(source, target string, mode os.FileMode) error {
	if err := os.MkdirAll(target, mode); err != nil {
		return fmt.Errorf("create recovery directory %s: %w", target, err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("read recovery directory %s: %w", source, err)
	}
	for _, entry := range entries {
		if err := copyRecoveryPath(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
