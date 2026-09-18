package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/kjanat/udm-iptv/internal/filemode"
)

const lockName = ".lock"

var errOperationInProgress = errors.New("another udm-iptv installation, upgrade, configuration or removal is in progress")

// AcquireLock serialises the operations that change an installation under
// stateDir. A held lock is refused rather than waited for, so a maintainer
// script that apt runs on behalf of a locked operation fails instead of
// waiting on its parent. The returned function releases the lock.
func AcquireLock(stateDir string) (func() error, error) {
	if err := validateStatePath(stateDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, filemode.PrivateDir); err != nil {
		return nil, fmt.Errorf("create state directory %s: %w", stateDir, err)
	}
	fd, err := unix.Open(filepath.Join(stateDir, lockName), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, filemode.PrivateFile)
	if err != nil {
		return nil, fmt.Errorf("open operation lock in %s: %w", stateDir, err)
	}
	file := os.NewFile(uintptr(fd), lockName)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.Join(errOperationInProgress, closeErr)
		}

		return nil, errors.Join(fmt.Errorf("lock %s: %w", stateDir, err), closeErr)
	}

	return func() error {
		if err := unix.Flock(fd, unix.LOCK_UN); err != nil {
			return errors.Join(fmt.Errorf("unlock %s: %w", stateDir, err), file.Close())
		}

		return file.Close()
	}, nil
}
