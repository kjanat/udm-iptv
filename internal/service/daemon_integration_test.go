//go:build linux && integration

package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/filemode"
	"github.com/kjanat/udm-iptv/internal/network"
)

const (
	daemonHelperMarker = "process-daemon"
	daemonAcquiring    = "DHCP_WAIT"
	daemonReturned     = "DAEMON_RETURN "
	daemonWait         = 10 * time.Second
)

// TestDaemonHelper runs Daemon.Run as a child process of the ownership test.
func TestDaemonHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != daemonHelperMarker {
		return
	}
	daemon := &Daemon{ConfigPath: os.Getenv("UDM_IPTV_CONFIG"), StateDir: os.Getenv("UDM_IPTV_STATE_DIR"), Out: os.Stdout, Err: os.Stderr}
	err := daemon.Run(t.Context())
	_, _ = fmt.Fprintf(os.Stdout, "%s%v\n", daemonReturned, err)
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

type daemonProcess struct {
	command   *exec.Cmd
	acquiring chan struct{}
	done      chan struct{}
	stderr    *bytes.Buffer
	result    string
	waitErr   error
}

// CI runs this test with privileges; every change lives in private network
// and mount namespaces.
func TestSecondDaemonCannotTakeTheIPTVNetwork(t *testing.T) {
	enterPrivateRuntime(t)
	if err := netlink.LinkAdd(&netlink.Dummy{Name: "wan-test"}); err != nil {
		t.Fatal(err)
	}
	configPath, stateDir, target := daemonFixture(t)

	first := startDaemon(t, configPath, stateDir)
	created := waitAcquiring(t, first, target)
	owner := assertOwnedBy(t, first, created)

	second := startDaemon(t, configPath, stateDir)
	assertRefused(t, second, first)
	assertLinkIndex(t, target, created.Attrs().Index)
	if still, err := ReadOwner(); err != nil || still != owner {
		t.Fatalf("owner record after refused run: %+v (%v)", still, err)
	}

	stopDaemon(t, first)
	assertReleased(t, target)

	third := startDaemon(t, configPath, stateDir)
	assertOwnedBy(t, third, waitAcquiring(t, third, target))
	stopDaemon(t, third)
}

func assertOwnedBy(t *testing.T, process *daemonProcess, created netlink.Link) Owner {
	t.Helper()
	owner, err := ReadOwner()
	if err != nil {
		t.Fatal(err)
	}
	pid := process.command.Process.Pid
	if owner.PID != pid || owner.LinkIndex != created.Attrs().Index || owner.Interface != created.Attrs().Name {
		t.Fatalf("owner record %+v; run PID %d created %s index %d", owner, pid, created.Attrs().Name, created.Attrs().Index)
	}

	return owner
}

func assertRefused(t *testing.T, refused, holder *daemonProcess) {
	t.Helper()
	pid := holder.command.Process.Pid
	result := waitReturned(t, refused)
	if !strings.Contains(result, errDaemonRunning.Error()) || !strings.Contains(result, fmt.Sprintf("PID %d", pid)) {
		t.Fatalf("second run ended with %q; expected refusal naming PID %d", result, pid)
	}
	if err := holder.command.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("owning run did not survive the refused run: %v", err)
	}
}

func assertReleased(t *testing.T, target string) {
	t.Helper()
	if _, err := netlink.LinkByName(target); err == nil {
		t.Fatal("the owning run's shutdown left its VLAN")
	}
	if _, err := ReadOwner(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner record after shutdown: %v", err)
	}
}

// enterPrivateRuntime moves the test thread into private network and mount
// namespaces with an empty tmpfs on /run. Children fork from that thread and
// inherit them. The thread stays locked and dies with the test goroutine.
func enterPrivateRuntime(t *testing.T) {
	t.Helper()
	runtime.LockOSThread()
	if err := unix.Unshare(unix.CLONE_NEWNET | unix.CLONE_NEWNS); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", "/run", "tmpfs", 0, ""); err != nil {
		t.Fatal(err)
	}
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatal(err)
	}
}

func daemonFixture(t *testing.T) (string, string, string) {
	t.Helper()
	stateDir := t.TempDir()
	binaries := filepath.Join(stateDir, "stand-ins")
	for name, script := range map[string]string{
		"udhcpc":   "#!/bin/sh\necho " + daemonAcquiring + "\nexec sleep 60\n",
		"iptables": "#!/bin/sh\nexit 0\n",
	} {
		if err := atomicfile.Write(filepath.Join(binaries, name), []byte(script), filemode.PrivateExecutable); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binaries+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NOTIFY_SOCKET", "")
	value := config.DefaultKPN()
	value.WAN.Interface = "wan-test"
	value.WAN.NATDestinations = nil
	value.LAN.Interfaces = []string{"lo"}
	value.Telemetry = config.Telemetry{}
	configPath := filepath.Join(stateDir, "config.json")
	if err := config.Save(configPath, value); err != nil {
		t.Fatal(err)
	}

	return configPath, stateDir, network.Target(value)
}

func startDaemon(t *testing.T, configPath, stateDir string) *daemonProcess {
	t.Helper()
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestDaemonHelper$", "--", daemonHelperMarker)
	command.Env = append(os.Environ(), "UDM_IPTV_CONFIG="+configPath, "UDM_IPTV_STATE_DIR="+stateDir)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	process := &daemonProcess{command: command, acquiring: make(chan struct{}), done: make(chan struct{}), stderr: new(bytes.Buffer)}
	command.Stderr = process.stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	go process.follow(stdout)
	t.Cleanup(func() {
		select {
		case <-process.done:
			return
		default:
		}
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-process.done
	})

	return process
}

func (process *daemonProcess) follow(stdout io.Reader) {
	defer close(process.done)
	scanner := bufio.NewScanner(stdout)
	acquiring := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == daemonAcquiring && !acquiring {
			acquiring = true
			close(process.acquiring)
		}
		if rest, ok := strings.CutPrefix(line, daemonReturned); ok {
			process.result = rest
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	process.waitErr = process.command.Wait()
}

func waitAcquiring(t *testing.T, process *daemonProcess, target string) netlink.Link {
	t.Helper()
	select {
	case <-process.acquiring:
	case <-process.done:
		t.Fatalf("daemon ended before acquiring DHCP: %s (%v)\n%s", process.result, process.waitErr, process.stderr)
	case <-time.After(daemonWait):
		t.Fatalf("daemon did not start its DHCP client\n%s", process.stderr)
	}
	link, err := netlink.LinkByName(target)
	if err != nil {
		t.Fatal(err)
	}

	return link
}

func waitReturned(t *testing.T, process *daemonProcess) string {
	t.Helper()
	select {
	case <-process.done:
	case <-time.After(daemonWait):
		t.Fatalf("daemon did not end\n%s", process.stderr)
	}

	return process.result
}

func stopDaemon(t *testing.T, process *daemonProcess) {
	t.Helper()
	if err := process.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	result := waitReturned(t, process)
	if !strings.Contains(result, context.Canceled.Error()) {
		t.Fatalf("stopped daemon returned %q\n%s", result, process.stderr)
	}
}

func assertLinkIndex(t *testing.T, name string, index int) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatalf("%s is gone: %v", name, err)
	}
	if link.Attrs().Index != index {
		t.Fatalf("%s is index %d; the owning run created %d", name, link.Attrs().Index, index)
	}
}
