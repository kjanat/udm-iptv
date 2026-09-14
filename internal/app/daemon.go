package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	sdnotify "github.com/coreos/go-systemd/v22/daemon"
	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/spf13/cobra"
)

const runtimeStatePath = "/run/udm-iptv/state.json"

type runtimeState struct {
	StartedAt time.Time `json:"startedAt"`
	Proxy     string    `json:"proxy"`
	ProxyPID  int       `json:"proxyPID"`
	Target    string    `json:"target"`
}

func (application *Application) daemonCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "daemon", Short: "Run the IPTV service", Hidden: true, Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			return application.runDaemon(command.Context())
		},
	}
	return command
}

func (application *Application) runDaemon(parent context.Context) error {
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
	var dhcpDone <-chan error
	if value.WAN.DHCP {
		_, _ = sdnotify.SdNotify(false, "STATUS=Waiting for the IPTV DHCP lease")
		if err := network.ResetLease(link); err != nil {
			return fmt.Errorf("reset previous DHCP lease: %w", err)
		}
		dhcpDone, err = application.startDHCP(ctx, value)
		if err != nil {
			return err
		}
	} else if err := network.ApplyStatic(value, link); err != nil {
		return err
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
	if err := atomicWrite(proxyConfigPath, []byte(proxyConfig), 0o600); err != nil {
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
	proxy := exec.CommandContext(ctx, value.Proxy.Program, arguments...)
	proxy.Stdout, proxy.Stderr = application.Out, application.Err
	proxy.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	configureGracefulStop(proxy)
	if err := proxy.Start(); err != nil {
		return fmt.Errorf("start %s: %w", value.Proxy.Program, err)
	}
	state := runtimeState{StartedAt: time.Now().UTC(), Proxy: value.Proxy.Program, ProxyPID: proxy.Process.Pid, Target: network.Target(value)}
	stateData, _ := json.MarshalIndent(state, "", "  ")
	if err := atomicWrite(runtimeStatePath, append(stateData, '\n'), 0o644); err != nil {
		_ = proxy.Process.Kill()
		return err
	}
	defer removeIgnoringError(runtimeStatePath)
	proxyDone := make(chan error, 1)
	go func() { proxyDone <- proxy.Wait() }()
	select {
	case proxyErr := <-proxyDone:
		if ctx.Err() != nil {
			return nil
		}
		if proxyErr != nil {
			return fmt.Errorf("%s exited: %w", value.Proxy.Program, proxyErr)
		}
		return errors.New("multicast proxy exited unexpectedly")
	case dhcpErr := <-dhcpDone:
		if ctx.Err() != nil {
			return nil
		}
		return unexpectedProcessExit("DHCP client", dhcpErr)
	case <-time.After(250 * time.Millisecond):
	}
	_, _ = sdnotify.SdNotify(false, sdnotify.SdNotifyReady)
	_, _ = sdnotify.SdNotify(false, "STATUS=IPTV proxy is running")
	select {
	case <-ctx.Done():
		_, _ = sdnotify.SdNotify(false, sdnotify.SdNotifyStopping)
		return nil
	case proxyErr := <-proxyDone:
		if ctx.Err() != nil {
			return nil
		}
		if proxyErr != nil {
			return fmt.Errorf("%s exited: %w", value.Proxy.Program, proxyErr)
		}
		return errors.New("multicast proxy exited unexpectedly")
	case dhcpErr := <-dhcpDone:
		if ctx.Err() != nil {
			return nil
		}
		return unexpectedProcessExit("DHCP client", dhcpErr)
	}
}

func unexpectedProcessExit(name string, err error) error {
	if err != nil {
		return fmt.Errorf("%s exited unexpectedly: %w", name, err)
	}
	return fmt.Errorf("%s exited unexpectedly", name)
}

func (application *Application) startDHCP(ctx context.Context, value config.Config) (<-chan error, error) {
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
	if err := client.Start(); err != nil {
		return nil, fmt.Errorf("start DHCP client: %w", err)
	}
	clientDone := make(chan error, 1)
	go func() { clientDone <- client.Wait() }()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-clientDone:
			if err == nil {
				return nil, errors.New("DHCP client exited before assigning an address")
			}
			return nil, fmt.Errorf("DHCP client exited before assigning an address: %w", err)
		case <-deadline.C:
			return nil, errors.New("DHCP did not assign an address within 30 seconds")
		case <-ticker.C:
			link, err := net.InterfaceByName(network.Target(value))
			if err != nil {
				continue
			}
			addresses, err := link.Addrs()
			if err == nil && hasIPv4Address(addresses) {
				return clientDone, nil
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

func (application *Application) dhcpHookCommand() *cobra.Command {
	var allowDefaultRoute bool
	command := &cobra.Command{
		Use: "dhcp-hook ACTION", Hidden: true, Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, arguments []string) error {
			if value, err := config.Load(application.ConfigPath); err == nil {
				allowDefaultRoute = value.WAN.AllowDefaultRoute
			}
			lease, err := network.LeaseFromEnvironment(arguments[0])
			if err != nil {
				return err
			}
			switch arguments[0] {
			case "deconfig", "bound", "renew":
				return network.ApplyLease(lease, allowDefaultRoute)
			case "leasefail":
				return errors.New("DHCP lease acquisition failed")
			case "nak":
				return errors.New("DHCP server rejected the lease")
			default:
				return fmt.Errorf("unsupported udhcpc action %q", arguments[0])
			}
		},
	}
	command.Flags().BoolVar(&allowDefaultRoute, "allow-default-route", false, "allow DHCP router fallback when RFC3442 routes are absent")
	return command
}

func (application *Application) waitHealthy(ctx context.Context, startup, stable time.Duration) error {
	connection, err := systemd.NewSystemConnectionContext(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	deadline := time.Now().Add(startup)
	var initial runtimeState
	for time.Now().Before(deadline) {
		properties, propertyErr := connection.GetUnitPropertiesContext(ctx, "udm-iptv.service")
		state, stateErr := readRuntimeState()
		if propertyErr == nil && stateErr == nil && properties["ActiveState"] == "active" && processExists(state.ProxyPID) {
			initial = state
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	if initial.ProxyPID == 0 {
		return fmt.Errorf("service did not become ready within %s", startup)
	}
	timer := time.NewTimer(stable)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	current, err := readRuntimeState()
	if err != nil || current.ProxyPID != initial.ProxyPID || !processExists(current.ProxyPID) {
		return errors.New("proxy did not remain stable")
	}
	return nil
}

func readRuntimeState() (runtimeState, error) {
	data, err := os.ReadFile(runtimeStatePath)
	if err != nil {
		return runtimeState{}, err
	}
	var state runtimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return runtimeState{}, err
	}
	return state, nil
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}

func parseUint(value any) uint64 {
	switch typed := value.(type) {
	case uint64:
		return typed
	case uint32:
		return uint64(typed)
	case uint:
		return uint64(typed)
	case int:
		if typed >= 0 {
			return uint64(typed)
		}
	case int32:
		if typed >= 0 {
			return uint64(typed)
		}
	case int64:
		if typed >= 0 {
			return uint64(typed)
		}
	case string:
		parsed, _ := strconv.ParseUint(typed, 10, 64)
		return parsed
	}
	return 0
}
