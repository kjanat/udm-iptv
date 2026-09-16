package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/telemetry"

	sdnotify "github.com/coreos/go-systemd/v22/daemon"
	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/kjanat/udm-iptv/internal/runtimebundle"
)

// Daemon supervises the IPTV network and proxy lifecycle.
type Daemon struct {
	ConfigPath, StateDir string
	Out, Err             io.Writer
	Monitor              *telemetry.Reporter
}

const runtimeStatePath = "/run/udm-iptv/state.json"

type RuntimeState struct {
	StartedAt time.Time `json:"startedAt"`
	Proxy     string    `json:"proxy"`
	ProxyPID  int       `json:"proxyPID"`
	Target    string    `json:"target"`
}

func (application *Daemon) Run(parent context.Context) (result error) {
	_, _ = sdnotify.SdNotify(false, "STATUS=Loading configuration")
	value, err := config.Load(application.ConfigPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	link, err := network.EnsureLink(value)
	if err != nil {
		return err
	}
	defer func() { _ = network.RemoveNAT(value) }()
	var dhcp *managedProcess
	var staticFailure <-chan error
	defer func() { result = errors.Join(result, dhcp.stop()) }()
	if value.WAN.DHCP {
		_, _ = sdnotify.SdNotify(false, "STATUS=Waiting for the IPTV DHCP lease")
		err := network.ResetLease(link)
		if err != nil {
			return fmt.Errorf("reset previous DHCP lease: %w", err)
		}
		dhcp, err = application.startDHCP(ctx, value)
		if err != nil {
			return err
		}
	} else {
		staticFailure, err = startStaticReconciler(ctx, value, link)
		if err != nil {
			return err
		}
		err := network.ApplyStatic(value, link)
		if err != nil {
			return err
		}
	}
	if err := network.EnsureNAT(value); err != nil {
		return err
	}
	proxyConfig, err := renderProxyConfig(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(runtimeStatePath), 0o755); err != nil {
		return err
	}
	proxyConfigPath := "/run/udm-iptv/proxy.conf"
	if err := atomicfile.Write(proxyConfigPath, []byte(proxyConfig), 0o600); err != nil {
		return err
	}
	defer removeIgnoringError(proxyConfigPath)
	arguments := []string{}
	if value.Proxy.Program == "improxy" {
		if value.Proxy.Debug {
			arguments = append(arguments, "-d", "5")
		}
		arguments = append(arguments, "-c", proxyConfigPath)
	} else {
		arguments = append(arguments, "-n")
		if value.Proxy.Debug {
			arguments = append(arguments, "-d", "-v")
		}
		arguments = append(arguments, proxyConfigPath)
	}
	proxyBinary, lookupErr := exec.LookPath(value.Proxy.Program)
	if lookupErr != nil {
		proxyBinary, arguments, err = runtimebundle.Command(application.StateDir, value.Proxy.Program, arguments)
		if err != nil {
			return err
		}
	}
	proxy := exec.CommandContext(ctx, proxyBinary, arguments...)
	proxy.Stdout, proxy.Stderr = application.Out, application.Err
	proxy.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	configureGracefulStop(proxy)
	state := RuntimeState{StartedAt: time.Now().UTC(), Proxy: value.Proxy.Program, Target: network.Target(value)}
	process, err := startProxy(proxy, runtimeStatePath, state)
	if err != nil {
		return fmt.Errorf("start %s: %w", value.Proxy.Program, err)
	}
	defer func() { result = errors.Join(result, process.stop()) }()
	defer removeIgnoringError(runtimeStatePath)
	select {
	case <-process.done:
		if ctx.Err() != nil {
			return nil
		}
		if process.err != nil {
			return fmt.Errorf("%s exited: %w", value.Proxy.Program, process.err)
		}

		return errors.New("multicast proxy exited unexpectedly")
	case <-processDone(dhcp):
		if ctx.Err() != nil {
			return nil
		}

		return unexpectedProcessExit("DHCP client", dhcp.err)
	case staticErr := <-staticFailure:
		if ctx.Err() != nil {
			return nil
		}

		return fmt.Errorf("reconcile static IPTV network: %w", staticErr)
	case <-time.After(250 * time.Millisecond):
	}
	_, _ = sdnotify.SdNotify(false, sdnotify.SdNotifyReady)
	_, _ = sdnotify.SdNotify(false, "STATUS=IPTV proxy is running")
	stopMetrics := application.startTelemetryMetrics(ctx)
	defer stopMetrics()
	select {
	case <-ctx.Done():
		_, _ = sdnotify.SdNotify(false, sdnotify.SdNotifyStopping)

		return nil
	case <-process.done:
		if ctx.Err() != nil {
			return nil
		}
		if process.err != nil {
			return fmt.Errorf("%s exited: %w", value.Proxy.Program, process.err)
		}

		return errors.New("multicast proxy exited unexpectedly")
	case <-processDone(dhcp):
		if ctx.Err() != nil {
			return nil
		}

		return unexpectedProcessExit("DHCP client", dhcp.err)
	case staticErr := <-staticFailure:
		if ctx.Err() != nil {
			return nil
		}

		return fmt.Errorf("reconcile static IPTV network: %w", staticErr)
	}
}

func startStaticReconciler(ctx context.Context, value config.Config, link netlink.Link) (<-chan error, error) {
	failures := make(chan error, 1)
	if value.WAN.StaticAddress == "" {
		return nil, nil
	}
	updates := make(chan netlink.AddrUpdate, 4)
	options := netlink.AddrSubscribeOptions{ErrorCallback: func(err error) {
		select {
		case failures <- err:
		default:
		}
	}}
	err := netlink.AddrSubscribeWithOptions(updates, ctx.Done(), options)
	if err != nil {
		return nil, fmt.Errorf("subscribe to address changes: %w", err)
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case update, ok := <-updates:
				if !ok {
					if ctx.Err() == nil {
						select {
						case failures <- errors.New("address change subscription closed"):
						default:
						}
					}

					return
				}
				if staticAddressDeleted(value.WAN.StaticAddress, link.Attrs().Index, update) {
					err := network.ApplyStatic(value, link)
					if err != nil {
						select {
						case failures <- err:
						default:
						}

						return
					}
				}
			}
		}
	}()

	return failures, nil
}

func staticAddressDeleted(configured string, linkIndex int, update netlink.AddrUpdate) bool {
	if update.NewAddr || update.LinkIndex != linkIndex {
		return false
	}
	prefix, err := netip.ParsePrefix(configured)
	if err != nil || !update.LinkAddress.IP.Equal(prefix.Addr().AsSlice()) {
		return false
	}
	bits, size := update.LinkAddress.Mask.Size()

	return size == 32 && bits == prefix.Bits()
}

func unexpectedProcessExit(name string, err error) error {
	if err != nil {
		return fmt.Errorf("%s exited unexpectedly: %w", name, err)
	}

	return fmt.Errorf("%s exited unexpectedly", name)
}

func (application *Daemon) startDHCP(ctx context.Context, value config.Config) (*managedProcess, error) {
	var done *managedProcess
	err := application.Monitor.Run(ctx, "dhcp.acquire", func(ctx context.Context) error {
		var err error
		done, err = application.startDHCPClient(ctx, value)

		return err
	})

	return done, err
}

func (application *Daemon) startDHCPClient(ctx context.Context, value config.Config) (*managedProcess, error) {
	hook := filepath.Join(application.StateDir, "bin", "udhcpc-hook")
	arguments := []string{"-f", "-R", "-p", "/run/udm-iptv/udhcpc.pid", "-s", hook, "-i", network.Target(value)}
	arguments = append(arguments, value.WAN.DHCPOptions...)
	binary := "udhcpc"
	if _, err := exec.LookPath(binary); err != nil {
		binary = "busybox"
		arguments = append([]string{"udhcpc"}, arguments...)
	}
	client := exec.CommandContext(ctx, binary, arguments...)
	client.Stdout, client.Stderr = application.Out, application.Err
	client.Env = append(os.Environ(), "UDM_IPTV_CONFIG="+application.ConfigPath)
	client.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	configureGracefulStop(client)
	process, err := startProcess(client)
	if err != nil {
		return nil, fmt.Errorf("start DHCP client: %w", err)
	}
	if err := waitDHCPLease(ctx, process, network.Target(value)); err != nil {
		return nil, errors.Join(err, process.stop())
	}
	return process, nil
}

func waitDHCPLease(ctx context.Context, process *managedProcess, target string) error {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-process.done:
			return unexpectedProcessExit("DHCP client before assigning an address", process.err)
		case <-deadline.C:
			return errors.New("DHCP did not assign an address within 30 seconds")
		case <-ticker.C:
			link, err := net.InterfaceByName(target)
			if err != nil {
				continue
			}
			addresses, err := link.Addrs()
			if err == nil && hasIPv4Address(addresses) {
				return nil
			}
		}
	}
}

func hasIPv4Address(addresses []net.Addr) bool {
	for _, address := range addresses {
		raw, _, found := strings.Cut(address.String(), "/")
		if found && net.ParseIP(raw).To4() != nil {
			return true
		}
	}

	return false
}

func configureGracefulStop(command *exec.Cmd) {
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}

		return syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	}
	command.WaitDelay = 5 * time.Second
}

func renderProxyConfig(value config.Config) (string, error) {
	var output strings.Builder
	target := network.Target(value)
	if value.Proxy.Program == "improxy" {
		fmt.Fprintf(&output, "igmp enable version %d\n", value.Proxy.IGMPVersion)
		output.WriteString("mld disable\n")
		if value.Proxy.QuickLeave {
			output.WriteString("quickleave enable\n")
		} else {
			output.WriteString("quickleave disable\n")
		}
		fmt.Fprintf(&output, "upstream %s\n", target)
		for _, name := range value.LAN.Interfaces {
			fmt.Fprintf(&output, "downstream %s\n", name)
		}

		return output.String(), nil
	}
	if value.Proxy.QuickLeave {
		output.WriteString("quickleave\n")
	}
	fmt.Fprintf(&output, "phyint %s upstream ratelimit 0 threshold 1\n", target)
	for _, prefix := range value.Proxy.SourceRanges {
		fmt.Fprintf(&output, "  altnet %s\n", prefix)
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	downstream := make(map[string]bool, len(value.LAN.Interfaces))
	for _, name := range value.LAN.Interfaces {
		downstream[name] = true
	}
	for _, iface := range interfaces {
		switch {
		case downstream[iface.Name]:
			fmt.Fprintf(&output, "phyint %s downstream ratelimit 0 threshold 1\n", iface.Name)
		case iface.Name != "lo" && iface.Name != target:
			fmt.Fprintf(&output, "phyint %s disabled\n", iface.Name)
		}
	}

	return output.String(), nil
}

func removeIgnoringError(path string) {
	_ = os.Remove(path)
}
