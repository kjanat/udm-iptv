package service

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestDaemonLockRefusesASecondRun(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "run")
	release, err := lockDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockDaemon(dir); !errors.Is(err, errDaemonRunning) {
		t.Fatalf("second run: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release, err = lockDaemon(dir)
	if err != nil {
		t.Fatalf("run after release: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonLockNamesTheHolder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	release, err := lockDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	}()
	owner, err := newOwner("iptv", &netlink.Dummy{Index: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeOwner(dir, owner); err != nil {
		t.Fatal(err)
	}
	_, err = lockDaemon(dir)
	if !errors.Is(err, errDaemonRunning) || !strings.Contains(err.Error(), "PID "+strconv.Itoa(os.Getpid())) {
		t.Fatalf("holder not named: %v", err)
	}
}

func TestRunTokensAreDistinct(t *testing.T) {
	t.Parallel()
	first, err := newOwner("iptv", &netlink.Dummy{Index: 7})
	if err != nil {
		t.Fatal(err)
	}
	second, err := newOwner("iptv", &netlink.Dummy{Index: 8})
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token || first.Token == "" {
		t.Fatalf("run tokens are not distinct: %q %q", first.Token, second.Token)
	}
}

func TestOwnerRecordRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	owner, err := newOwner("iptv", &netlink.Dummy{Index: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeOwner(dir, owner); err != nil {
		t.Fatal(err)
	}
	read, err := readOwner(dir)
	if err != nil {
		t.Fatal(err)
	}
	if read.Token != owner.Token || read.PID != os.Getpid() || read.Interface != "iptv" || read.LinkIndex != 7 || read.StartedAt.IsZero() {
		t.Fatalf("owner record = %+v", read)
	}
}

func TestHookOwnership(t *testing.T) {
	t.Parallel()
	held := Owner{Token: "run-b"}
	for name, test := range map[string]struct {
		owner   Owner
		readErr error
		token   string
		allowed bool
		err     error
	}{
		"own client":                 {held, nil, "run-b", true, nil},
		"previous run's client":      {held, nil, "run-a", false, nil},
		"untagged client while held": {held, nil, "", false, nil},
		"own client, record gone":    {Owner{}, os.ErrNotExist, "run-b", false, nil},
		"untagged client, no owner":  {Owner{}, os.ErrNotExist, "", true, nil},
		"unreadable record":          {Owner{}, os.ErrPermission, "run-b", false, os.ErrPermission},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			allowed, err := hookAllowed(test.owner, test.readErr, test.token)
			if allowed != test.allowed || !errors.Is(err, test.err) {
				t.Fatalf("allowed=%v err=%v", allowed, err)
			}
		})
	}
}
