package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/filemode"
)

const (
	runtimeDir = "/run/udm-iptv"
	lockFile   = "daemon.lock"
	ownerFile  = "owner.json"
	// OwnerEnvironment carries the owning run's token to the DHCP client and its hook.
	OwnerEnvironment = "UDM_IPTV_OWNER"
	ownerTokenBytes  = 16
)

var errDaemonRunning = errors.New("another udm-iptv daemon owns the IPTV network; stop it before starting another")

// Owner identifies the daemon run that holds the IPTV network.
type Owner struct {
	Token     string    `json:"token"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
	Interface string    `json:"interface"`
	LinkIndex int       `json:"linkIndex"`
}

// lockDaemon takes the exclusive daemon lock in dir, which the kernel
// releases when the holder exits. The returned function releases it.
func lockDaemon(dir string) (func() error, error) {
	if err := os.MkdirAll(dir, filemode.SharedDir); err != nil {
		return nil, fmt.Errorf("create the daemon runtime directory: %w", err)
	}
	path := filepath.Join(dir, lockFile)
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, filemode.PrivateFile)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.Join(describeHolder(dir), closeErr)
		}

		return nil, errors.Join(fmt.Errorf("lock %s: %w", path, err), closeErr)
	}

	return func() error {
		if err := unix.Flock(fd, unix.LOCK_UN); err != nil {
			return errors.Join(fmt.Errorf("unlock %s: %w", path, err), file.Close())
		}

		return file.Close()
	}, nil
}

func describeHolder(dir string) error {
	holder, err := readOwner(dir)
	if err != nil {
		return errDaemonRunning
	}

	return fmt.Errorf("%w (PID %d since %s)", errDaemonRunning, holder.PID, holder.StartedAt.Format(time.RFC3339))
}

func newOwner(target string, link netlink.Link) (Owner, error) {
	var raw [ownerTokenBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Owner{}, fmt.Errorf("generate the daemon run token: %w", err)
	}

	return Owner{
		Token: hex.EncodeToString(raw[:]), PID: os.Getpid(), StartedAt: time.Now().UTC(),
		Interface: target, LinkIndex: link.Attrs().Index,
	}, nil
}

func writeOwner(dir string, owner Owner) error {
	data, err := json.Marshal(owner)
	if err != nil {
		return fmt.Errorf("encode the daemon owner record: %w", err)
	}
	if err := atomicfile.Write(filepath.Join(dir, ownerFile), data, filemode.SharedFile); err != nil {
		return fmt.Errorf("write the daemon owner record: %w", err)
	}

	return nil
}

// ReadOwner reads the record of the daemon run that holds the IPTV network.
func ReadOwner() (Owner, error) {
	return readOwner(runtimeDir)
}

func readOwner(dir string) (Owner, error) {
	path := filepath.Join(dir, ownerFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return Owner{}, fmt.Errorf("read %s: %w", path, err)
	}
	var owner Owner
	if err := json.Unmarshal(data, &owner); err != nil {
		return Owner{}, fmt.Errorf("parse %s: %w", path, err)
	}

	return owner, nil
}

// HookOwnership reports whether a DHCP hook invoked with token belongs to the
// daemon run that currently holds the IPTV network.
func HookOwnership(token string) (bool, error) {
	owner, err := ReadOwner()

	return hookAllowed(owner, err, token)
}

// A client without a token was started by a daemon from before run ownership.
func hookAllowed(owner Owner, readErr error, token string) (bool, error) {
	switch {
	case readErr == nil:
		return owner.Token == token, nil
	case errors.Is(readErr, os.ErrNotExist):
		return token == "", nil
	default:
		return false, readErr
	}
}
