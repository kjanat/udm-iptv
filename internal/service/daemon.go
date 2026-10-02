package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	sdnotify "github.com/coreos/go-systemd/v22/daemon"
	"github.com/vishvananda/netlink"

	"github.com/kjanat/udm-iptv/internal/atomicfile"
	"github.com/kjanat/udm-iptv/internal/config"
	"github.com/kjanat/udm-iptv/internal/filemode"
	"github.com/kjanat/udm-iptv/internal/network"
	"github.com/kjanat/udm-iptv/internal/proxycheck"
	"github.com/kjanat/udm-iptv/internal/runtimebundle"
	"github.com/kjanat/udm-iptv/internal/telemetry"
)

// Daemon supervises the IPTV network and proxy lifecycle.
type Daemon struct {
	ConfigPath, StateDir string
	Out, Err             io.Writer
	Monitor              *telemetry.Reporter
	Diagnostics          func(context.Context) (json.RawMessage, error)
}

const (
	runtimeStatePath = "/run/udm-iptv/state.json"
	proxyConfigPath  = "/run/udm-iptv/proxy.conf"
)

const (
	// signalGrace is a short pause after a stop signal before forced cleanup.
	signalGrace = 250 * time.Millisecond
	// addressUpdateBuffer bounds pending static-address change notifications.
	addressUpdateBuffer = 4
	// dhcpAcquireTimeout allows two complete discovery rounds (9s each),
	// separated by a 2s retry delay, before the 45s systemd startup deadline.
	dhcpAcquireTimeout = 30 * time.Second
	// dhcpLeasePoll is how often acquisition checks the hook's lease record.
	dhcpLeasePoll = 250 * time.Millisecond
	// processWaitDelay bounds a killed process's exit after Wait's pipes close.
	processWaitDelay = 5 * time.Second
)

var (
	errProxyExited               = errors.New("multicast proxy exited unexpectedly")
	errProcessExited             = errors.New("exited unexpectedly")
	errAddressSubscriptionClosed = errors.New("address change subscription closed")
	errDHCPLeaseTimeout          = errors.New("the DHCP hook did not apply a lease within 30 seconds")
)

// RuntimeState is the running proxy's identity, read by `udm-iptv status`.
type RuntimeState struct {
	StartedAt time.Time `json:"startedAt"`
	Proxy     string    `json:"proxy"`
	ProxyPID  int       `json:"proxyPID"`
	Target    string    `json:"target"`
}

// Run brings up the IPTV network and multicast proxy, and supervises them
// until the context is cancelled or either exits unexpectedly.
func (application *Daemon) Run(parent context.Context) (result error) {
	_, _ = sdnotify.SdNotify(false, "STATUS=Loading configuration")
	value, addressing, err := startupConfiguration(parent, application.ConfigPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	link, owner, releaseNetwork, err := claimNetwork(value)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, releaseNetwork()) }()
	restoreIPv6, err := network.EnableIPv6Multicast(value)
	defer func() { result = errors.Join(result, restoreIPv6()) }()
	if err != nil {
		return fmt.Errorf("prepare IPv6 multicast: %w", err)
	}
	defer func() {
		removed, _ := network.RemoveNAT(value)
		application.logRemovedNAT(context.WithoutCancel(ctx), removed)
	}()
	var dhcp *managedProcess
	defer func() { result = errors.Join(result, dhcp.stop()) }()
	dhcp, networkFailure, err := application.startConnection(ctx, value, addressing, link, owner.Token)
	if err != nil {
		return err
	}
	removed, err := network.EnsureNAT(value)
	application.logRemovedNAT(ctx, removed)
	if err != nil {
		return fmt.Errorf("configure IPTV NAT: %w", err)
	}
	if err := writeProxyConfig(value); err != nil {
		return err
	}
	defer removeIgnoringError(proxyConfigPath)
	process, stopProxy, err := application.launchProxy(ctx, value)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, stopProxy()) }()
	defer removeIgnoringError(runtimeStatePath)

	return application.supervise(ctx, supervised{
		program: value.Proxy.Program, config: value, proxy: process, dhcp: dhcp, network: networkFailure,
	})
}

// startupConfiguration checks prerequisites before any network mutation.
func startupConfiguration(ctx context.Context, path string) (config.Config, config.Addressing, error) {
	value, err := config.Load(path)
	if err != nil {
		return config.Config{}, config.Addressing{}, fmt.Errorf("load configuration: %w", err)
	}
	addressing, err := value.WAN.Addressing()
	if err != nil {
		return config.Config{}, config.Addressing{}, fmt.Errorf("resolve WAN addressing: %w", err)
	}
	if err := proxycheck.Check(ctx); err != nil {
		return config.Config{}, config.Addressing{}, fmt.Errorf("check multicast proxy before startup: %w", err)
	}
	return value, addressing, nil
}

// claimNetwork takes the daemon lock, prepares the IPTV interface and records
// this run as its owner. The returned function undoes all three, releasing
// the lock last.
func claimNetwork(value config.Config) (netlink.Link, Owner, func() error, error) {
	release, err := lockDaemon(runtimeDir)
	if err != nil {
		return nil, Owner{}, nil, fmt.Errorf("claim the IPTV network: %w", err)
	}
	link, err := network.EnsureLink(value)
	if err != nil {
		return nil, Owner{}, nil, errors.Join(fmt.Errorf("prepare the IPTV interface: %w", err), release())
	}
	owner, err := newOwner(network.Target(value), link)
	if err == nil {
		err = writeOwner(runtimeDir, owner)
	}
	if err != nil {
		return nil, Owner{}, nil, errors.Join(err, network.RemoveLink(value, link), release())
	}

	return link, owner, func() error {
		removeIgnoringError(filepath.Join(runtimeDir, ownerFile))

		return errors.Join(network.RemoveStaticRoutes(link), network.RemoveLink(value, link), release())
	}, nil
}

// iptables discards a rule's counters with the rule.
func (application *Daemon) logRemovedNAT(ctx context.Context, removed []network.NATRule) {
	if len(removed) == 0 {
		return
	}
	log := application.Monitor.LineWriter(ctx, "nat")
	output := io.MultiWriter(application.Out, log)
	for _, rule := range removed {
		_, _ = fmt.Fprintf(output, "NAT rule removed: %s\n", rule)
	}
}

// supervised names the ways a started run can end.
type supervised struct {
	program string
	config  config.Config
	proxy   *managedProcess
	dhcp    *managedProcess
	network <-chan error
}

// supervise waits out a settling period before reporting readiness, so a
// proxy that fails immediately is reported as a startup failure.
func (application *Daemon) supervise(ctx context.Context, sources supervised) error {
	select {
	case <-sources.proxy.done:
		return sources.proxyStopped(ctx)
	case <-processDone(sources.dhcp):
		return sources.dhcpStopped(ctx)
	case cause := <-sources.network:
		return networkFailed(ctx, cause)
	case <-time.After(signalGrace):
	}
	_, _ = sdnotify.SdNotify(false, sdnotify.SdNotifyReady)
	_, _ = sdnotify.SdNotify(false, "STATUS=IPTV proxy is running")
	application.Monitor.Ready(ctx)
	stopMetrics := application.startTelemetryMetrics(ctx, sources.config)
	defer stopMetrics()
	select {
	case <-ctx.Done():
		_, _ = sdnotify.SdNotify(false, sdnotify.SdNotifyStopping)

		return nil
	case <-sources.proxy.done:
		return sources.proxyStopped(ctx)
	case <-processDone(sources.dhcp):
		return sources.dhcpStopped(ctx)
	case cause := <-sources.network:
		return networkFailed(ctx, cause)
	}
}

// stopping reports whether the daemon is shutting down. A stop signal sent
// to the whole process group reaches the children first; a child's exit
// therefore waits signalGrace for this process to observe the signal.
func stopping(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	grace := time.NewTimer(signalGrace)
	defer grace.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-grace.C:
		return false
	}
}

func (sources supervised) proxyStopped(ctx context.Context) error {
	if stopping(ctx) {
		return nil
	}
	if sources.proxy.err != nil {
		return fmt.Errorf("%s exited: %w", sources.program, sources.proxy.err)
	}

	return errProxyExited
}

func (sources supervised) dhcpStopped(ctx context.Context) error {
	if stopping(ctx) {
		return nil
	}

	return unexpectedProcessExit("DHCP client", sources.dhcp.err)
}

func networkFailed(ctx context.Context, cause error) error {
	if stopping(ctx) {
		return nil
	}

	return fmt.Errorf("keep the IPTV network configured: %w", cause)
}

func writeProxyConfig(value config.Config) error {
	rendered, err := renderProxyConfig(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(runtimeStatePath), filemode.SharedDir); err != nil {
		return fmt.Errorf("create the daemon runtime directory: %w", err)
	}
	if err := atomicfile.Write(proxyConfigPath, []byte(rendered), filemode.PrivateFile); err != nil {
		return fmt.Errorf("write the proxy configuration: %w", err)
	}

	return nil
}

// launchProxy starts the multicast proxy and returns it with the function
// that stops it and closes its output.
func (application *Daemon) launchProxy(ctx context.Context, value config.Config) (*managedProcess, func() error, error) {
	proxy, output, err := application.proxyCommand(ctx, value)
	if err != nil {
		return nil, nil, err
	}
	state := RuntimeState{StartedAt: time.Now().UTC(), Proxy: value.Proxy.Program, Target: network.Target(value)}
	process, err := startProxy(proxy, runtimeStatePath, state)
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("start %s: %w", value.Proxy.Program, err), output.finish())
	}
	if err := output.started(); err != nil {
		return nil, nil, errors.Join(err, process.stop(), output.finish())
	}

	return process, func() error { return errors.Join(process.stop(), output.finish()) }, nil
}

func (application *Daemon) proxyCommand(ctx context.Context, value config.Config) (*exec.Cmd, *processOutput, error) {
	arguments := proxyArguments(value)
	binary, err := exec.LookPath(value.Proxy.Program)
	if err != nil {
		binary, arguments, err = runtimebundle.Command(application.StateDir, value.Proxy.Program, arguments)
		if err != nil {
			return nil, nil, fmt.Errorf("find %s: %w", value.Proxy.Program, err)
		}
	}
	proxy := exec.CommandContext(ctx, binary, arguments...)
	proxyLog := application.Monitor.LineWriter(ctx, "proxy")
	out := io.MultiWriter(application.Out, proxyLog)
	proxy.Stderr = io.MultiWriter(application.Err, proxyLog)
	output, err := attachTerminal(proxy, out)
	if err != nil {
		_, _ = fmt.Fprintf(application.Err, "%s output through a pipe: %v\n", value.Proxy.Program, err)
		proxy.Stdout = out
		output = &processOutput{}
	}
	proxy.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	configureGracefulStop(proxy)

	return proxy, output, nil
}

func proxyArguments(value config.Config) []string {
	if value.Proxy.Program == config.ProxyImproxy {
		arguments := []string{}
		if value.Proxy.Debug {
			arguments = append(arguments, "-d", "5")
		}

		return append(arguments, "-c", proxyConfigPath)
	}
	arguments := []string{"-n"}
	if value.Proxy.Debug {
		arguments = append(arguments, "-d", "-v")
	}

	return append(arguments, proxyConfigPath)
}

// startConnection brings up either the DHCP client or the static IPTV
// address, returning the channel that reports the mode's later failures.
func (application *Daemon) startConnection(ctx context.Context, value config.Config, addressing config.Addressing, link netlink.Link, owner string) (*managedProcess, <-chan error, error) {
	if addressing.DHCP() {
		return application.startDHCPConnection(ctx, value, link, owner)
	}
	var staticFailure <-chan error
	if addressing.Static().IsValid() {
		var err error
		staticFailure, err = startStaticReconciler(ctx, value, addressing, link)
		if err != nil {
			return nil, nil, err
		}
	}

	adopt, ownRoutes := application.staticRouteAdoption()
	if err := network.ApplyStatic(value, addressing, link, adopt); err != nil {
		return nil, staticFailure, fmt.Errorf("apply the static IPTV network: %w", err)
	}
	if err := ownRoutes(); err != nil {
		return nil, staticFailure, err
	}

	return nil, staticFailure, nil
}

func (application *Daemon) startDHCPConnection(ctx context.Context, value config.Config, link netlink.Link, owner string) (*managedProcess, <-chan error, error) {
	_, _ = sdnotify.SdNotify(false, "STATUS=Waiting for the IPTV DHCP lease")
	if err := network.ResetLease(link); err != nil {
		return nil, nil, fmt.Errorf("reset previous DHCP lease: %w", err)
	}
	adopt, ownRoutes := application.staticRouteAdoption()
	if err := network.ApplyStaticRoutes(value, link, adopt); err != nil {
		return nil, nil, fmt.Errorf("apply the configured IPTV routes: %w", err)
	}
	if err := ownRoutes(); err != nil {
		return nil, nil, err
	}
	dhcp, err := application.startDHCP(ctx, value, owner)
	if err != nil {
		return dhcp, nil, err
	}
	lost, err := watchLeaseAddress(ctx, link)
	if err != nil {
		return dhcp, nil, fmt.Errorf("watch the IPTV DHCP address: %w", err)
	}

	return dhcp, lost, nil
}

// staticRoutesOwned marks a state directory whose configured routes carry
// this program's protocol. Before it exists, the first start takes over the
// unmarked routes an earlier version left on the uplink.
const staticRoutesOwned = "static-routes.owned"

func (application *Daemon) staticRouteAdoption() (bool, func() error) {
	marker := filepath.Join(application.StateDir, staticRoutesOwned)
	if _, err := os.Stat(marker); err == nil {
		return false, func() error { return nil }
	}

	return true, func() error {
		if err := atomicfile.Write(marker, []byte{}, filemode.PrivateFile); err != nil {
			return fmt.Errorf("record the configured route ownership: %w", err)
		}

		return nil
	}
}

func startStaticReconciler(ctx context.Context, value config.Config, addressing config.Addressing, link netlink.Link) (<-chan error, error) {
	failures := make(chan error, 1)
	updates := make(chan netlink.AddrUpdate, addressUpdateBuffer)
	options := netlink.AddrSubscribeOptions{ErrorCallback: func(err error) { reportFailure(failures, err) }}
	err := netlink.AddrSubscribeWithOptions(updates, ctx.Done(), options)
	if err != nil {
		return nil, fmt.Errorf("subscribe to address changes: %w", err)
	}
	go restoreStaticAddress(ctx, value, addressing, link, updates, failures)

	return failures, nil
}

// reportFailure keeps the first failure without blocking the netlink reader.
func reportFailure(failures chan<- error, cause error) {
	select {
	case failures <- cause:
	default:
	}
}

func restoreStaticAddress(ctx context.Context, value config.Config, addressing config.Addressing, link netlink.Link, updates <-chan netlink.AddrUpdate, failures chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-updates:
			if !ok {
				if ctx.Err() == nil {
					reportFailure(failures, errAddressSubscriptionClosed)
				}

				return
			}
			if !staticAddressDeleted(addressing.Static(), link.Attrs().Index, update) {
				continue
			}
			if err := network.ApplyStatic(value, addressing, link, false); err != nil {
				reportFailure(failures, err)

				return
			}
		}
	}
}

func staticAddressDeleted(prefix netip.Prefix, linkIndex int, update netlink.AddrUpdate) bool {
	if !prefix.IsValid() || update.NewAddr || update.LinkIndex != linkIndex {
		return false
	}
	if !update.LinkAddress.IP.Equal(prefix.Addr().AsSlice()) {
		return false
	}
	bits, size := update.LinkAddress.Mask.Size()

	return size == 32 && bits == prefix.Bits()
}

func unexpectedProcessExit(name string, err error) error {
	if err != nil {
		return fmt.Errorf("%s %w: %w", name, errProcessExited, err)
	}

	return fmt.Errorf("%s %w", name, errProcessExited)
}

func (application *Daemon) startDHCP(ctx context.Context, value config.Config, owner string) (*managedProcess, error) {
	var done *managedProcess
	err := application.Monitor.Run(ctx, "dhcp.acquire", func(ctx context.Context) error {
		var err error
		done, err = application.startDHCPClient(ctx, value, owner)

		return err
	})
	if err != nil {
		return done, fmt.Errorf("acquire the IPTV DHCP lease: %w", err)
	}

	return done, nil
}

func (application *Daemon) startDHCPClient(ctx context.Context, value config.Config, owner string) (*managedProcess, error) {
	hook := filepath.Join(application.StateDir, "bin", "udhcpc-hook")
	arguments := dhcpArguments(value, hook)
	binary := "udhcpc"
	if _, err := exec.LookPath(binary); err != nil {
		binary = "busybox"
		arguments = append([]string{"udhcpc"}, arguments...)
	}
	client := exec.CommandContext(ctx, binary, arguments...)
	dhcpLog := application.Monitor.LineWriter(ctx, "udhcpc")
	client.Stdout, client.Stderr = io.MultiWriter(application.Out, dhcpLog), io.MultiWriter(application.Err, dhcpLog)
	client.Env = dhcpEnvironment(application.ConfigPath, application.StateDir, owner)
	client.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	configureGracefulStop(client)
	process, err := startProcess(client)
	if err != nil {
		return nil, fmt.Errorf("start DHCP client: %w", err)
	}
	if err := waitDHCPLease(ctx, process, network.Target(value), owner); err != nil {
		return nil, errors.Join(err, process.stop())
	}
	return process, nil
}

// The hook records udhcpc's lower-case environment as lease options. A clean
// child environment prevents inherited credentials or even stale option names
// from being mistaken for data supplied by the DHCP server.
func dhcpEnvironment(configPath, stateDir, owner string) []string {
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"UDM_IPTV_CONFIG=" + configPath,
		"UDM_IPTV_STATE_DIR=" + stateDir,
		OwnerEnvironment + "=" + owner,
	}
	if metric, ok := os.LookupEnv("IF_METRIC"); ok {
		environment = append(environment, "IF_METRIC="+metric)
	}
	return environment
}

// dhcpArguments makes the retry policy explicit. User options come last so
// installations can override it, but acquisition still has a bounded deadline.
func dhcpArguments(value config.Config, hook string) []string {
	return slices.Concat([]string{"-f", "-R", "-t", "3", "-T", "3", "-A", "2", "-p", "/run/udm-iptv/udhcpc.pid", "-s", hook, "-i", network.Target(value)}, value.WAN.DHCPOptions)
}

// waitDHCPLease waits until the hook records that it applied a lease of
// this run to target. An address on the interface is not enough: the hook
// sets it before the routes, and a route it could not install is a failure.
func waitDHCPLease(ctx context.Context, process *managedProcess, target, owner string) error {
	deadline := time.NewTimer(dhcpAcquireTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(dhcpLeasePoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for the DHCP lease: %w", ctx.Err())
		case <-process.done:
			return unexpectedProcessExit("DHCP client before applying a lease", process.err)
		case <-deadline.C:
			return errDHCPLeaseTimeout
		case <-ticker.C:
			state, err := ReadLeaseState()
			if err != nil {
				continue
			}
			ready, err := leaseReady(state, target, owner)
			if err != nil {
				return err
			}
			if ready {
				return nil
			}
		}
	}
}

func configureGracefulStop(command *exec.Cmd) {
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}

		return syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	}
	command.WaitDelay = processWaitDelay
}

func renderProxyConfig(value config.Config) (string, error) {
	target := network.Target(value)
	if value.Proxy.Program == config.ProxyImproxy {
		return renderIMProxyConfig(value, target), nil
	}

	return renderIGMPProxyConfig(value, target)
}

func renderIMProxyConfig(value config.Config, target string) string {
	var output strings.Builder
	fmt.Fprintf(&output, "igmp enable version %d\n", value.Proxy.IGMPVersion)
	if value.Proxy.MLDVersion == 0 {
		output.WriteString("mld disable\n")
	} else {
		fmt.Fprintf(&output, "mld enable version %d\n", value.Proxy.MLDVersion)
	}
	if value.Proxy.QuickLeave {
		output.WriteString("quickleave enable\n")
	} else {
		output.WriteString("quickleave disable\n")
	}
	fmt.Fprintf(&output, "upstream %s\n", target)
	for _, name := range value.LAN.Interfaces {
		fmt.Fprintf(&output, "downstream %s\n", name)
	}

	return output.String()
}

func renderIGMPProxyConfig(value config.Config, target string) (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("list network interfaces: %w", err)
	}
	var output strings.Builder
	if value.Proxy.QuickLeave {
		output.WriteString("quickleave\n")
	}
	fmt.Fprintf(&output, "phyint %s upstream ratelimit 0 threshold 1\n", target)
	for _, prefix := range value.Proxy.SourceRanges {
		fmt.Fprintf(&output, "  altnet %s\n", prefix)
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
