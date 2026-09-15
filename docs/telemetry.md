# Optional telemetry

Telemetry is **on by default for new configurations**. The wizard asks
"Help improve udm-iptv?". Existing opt-outs are preserved; legacy imports remain off.
No Sentry account is needed. When enabled, reports go to the maintainer's Sentry
project through its EU ingest endpoint. The receiver can see the connection's
public source IP; this is not anonymous reporting.

The release workflow embeds the public DSN from the repository's `SENTRY_DSN`
Actions variable. Ordinary builds and fork releases have no endpoint unless
explicitly configured.
GoReleaser accepts `UDM_IPTV_SENTRY_DSN` at build time; direct Go builds can use
`-ldflags '-X github.com/kjanat/udm-iptv/internal/telemetry.DSN=<dsn>'`.
This is destination configuration, not secrecy: the DSN is readable from the
binary. Runtime `SENTRY_DSN` does not override it. Without a build endpoint,
enabled telemetry continues the IPTV command without reporting.

## Enable or disable

Use `udm-iptv configure` to toggle reporting, or:

```console
udm-iptv configure --non-interactive --telemetry=true
udm-iptv configure --non-interactive --telemetry-logs=false
udm-iptv configure --non-interactive --telemetry-trace-rate=1
udm-iptv configure --non-interactive --telemetry=false
```

The first command enables the saved product selection (all six on a new
configuration). Each product also has a separate flag: `--telemetry-errors`,
`--telemetry-logs`, `--telemetry-metrics`, `--telemetry-tracing`,
`--telemetry-presets`, and `--telemetry-network-identity`.
Selecting products does not enable the master switch. Tracing defaults to 10%
of operations; a rate of `1` selects all operations, subject to reporting limits.

Configuration changes restart an installed service. Disabling telemetry prevents
new events from being queued, including by an old daemon that has not stopped yet.
Previously queued or in-flight requests may finish; disabling does not delete
reports already received by Sentry.

## What is sent

- **Errors:** failed managed operations and panics caught on their executing
  goroutine, with wrapped/joined error types and their causal relationships.
  SDK-supported embedded stacks are retained; otherwise a stack is captured at
  the reporting point. Raw error and panic text stays local. Reporting-point
  stacks do not identify where a returned Go error was originally created.
  Panics in unrelated goroutines and
  forced process termination are not automatically captured.
- **Logs:** fixed lifecycle messages such as `dhcp.renew completed`. These are
  explicitly instrumented operations, not forwarded stdout, proxy output or journals.
- **Metrics:** operation completion/failure counts and durations; once a minute,
  daemon uptime, systemd restart count and host-wide multicast route/packet totals.
  Multicast counters can reset and may not reflect hardware-offloaded traffic.
  They do not establish whether a receiver has a picture.
- **Tracing:** sampled operation durations, including DHCP acquisition and service
  health checks. Start/end logs and errors retain the active operation's trace and
  span identifiers. Cancellation is recorded as cancelled, not as a failure;
  deadlines have a separate timeout status. The daemon's entire lifetime is not
  one long transaction.

Operational metadata includes the software release, known model/profile/proxy
names and numeric firmware. Preset research also attaches the random installation
ID to errors, logs and metrics to correlate failures with configuration history.
Credentials, MAC addresses, full configuration files, packet payloads, command
arguments and diagnostic captures are never attached.
Stack frames retain source basenames, functions and line numbers;
absolute paths, source excerpts and local variables are removed.

## Provider and configuration research

`--telemetry-presets` enables structured `installation.report` events with schema
version 1. Reports contain selected profile, VLAN, DHCP/static mode, default-route
policy, proxy, IGMP version, quickleave, debug setting and downstream count.
Shipped provider prefixes and custom public IPv4 networks from /8 through /24
are included; other prefixes are counted. Custom host routes and prefixes
overlapping private/special-use networks are omitted. Interface names, custom MACs, assigned/static
addresses, arbitrary DHCP options and route strings are excluded from snapshots.

Saved configurations have a revision, previous revision, changed field names and
last meaningful saved-change timestamp. A separate last-applied-change timestamp
advances only after installation/reconfiguration passes its service health check;
`applied_revision` identifies that configuration separately from the latest saved one.
An unchanged configuration or telemetry-preference change does not advance the
revision. Changes to omitted settings are reported as `unreported_settings`,
without their values. These comparisons use a local keyed fingerprint, never
sent to Sentry. First installation is reported after saving the reporting choice;
cancelling before saving, UI preview and installation dry-run send no research.

With `--telemetry-network-identity` also enabled, configuration reports include
the **full public egress IP and PTR hostname**. Discovery makes an HTTPS request
to [ipify](https://www.ipify.org/) and a reverse lookup through the system DNS
resolver, with a shared three-second deadline. This exposes the egress IP to
ipify and the queried address to the resolver. Lookup failure is recorded as
unavailable and never prevents IPTV configuration. No raw IP/PTR is stored in
the local research history or used as an installation ID or Sentry tag.

Known PTR suffixes produce a **low-confidence provider hint** with detection
method `ptr-suffix`; otherwise the provider is unknown. Selected profile remains
a separate field. VPNs, multi-WAN and wholesale access can make the egress
operator different from the IPTV provider. This is not proof of subscription.
Network identity is an optional addition to preset research, not a standalone
report: disabling preset research disables these lookups too.

One minute after daemon startup, then hourly, research records process uptime,
systemd restarts and whether the service is active. Together with correlated DHCP
and health-operation outcomes, this supports investigation of observed stability.
It does **not** establish an uninterrupted picture or user satisfaction; missing
reports, unchanged settings and a quiet process are not success signals.

Users can explicitly report their experience and optionally confirm their provider:

```sh
udm-iptv telemetry feedback working --provider kpn
udm-iptv telemetry feedback problems
udm-iptv telemetry feedback not-using
udm-iptv telemetry reset-id
```

Provider confirmation accepts profile IDs, `xs4all` or `freedom`; arbitrary free
text is not collected. Reset creates a new random identity and clears its local
configuration history. Running processes pick up the new identity. Already
queued events and previously received reports are not deleted by a reset.

History is stored atomically in mode-0600 `telemetry-research.json`, with a
cross-process lock and bounded reads. Corrupt or unavailable state suppresses
research rather than disrupting IPTV. Reports are best effort, not an audit log.

IP/PTR and correlated history are identifying data. Sentry project access and
retention must be managed by the maintainer; the CLI cannot enforce deletion of
remote records. No special shorter retention is implemented here. Review preset
candidates across independent installations and explicit feedback before changing
shipped defaults; this feature does not modify presets automatically.

## Limits and failure handling

Reporting is asynchronous, uses a 32-envelope transport queue and two-second HTTP
timeouts, and attempts a bounded two-second flush at command exit. Telemetry setup
or delivery failure does not change an IPTV operation's result.

Budgets are shared across processes through mode-0600 `telemetry-*.rate` files in
the state directory. They contain only time buckets and counters, and survive
daemon restarts. Errors are limited to 5/minute and 600/day; logs to 30/minute and
3,600/day; metrics to 60/minute and 7,200/day; traces to 20/minute and 2,400/day.
An unavailable budget file, concurrent lock holder, backward clock adjustment or
unreadable consent setting drops telemetry rather than blocking IPTV.
Research is limited to 5 events/minute and 600/day; network discovery to
1/minute and 120/day. There is no offline upload backlog of configuration reports.

Diagnostic captures remain local and are never uploaded by this integration.
Tests use a recording transport; running the test suite does not send test events
to the maintainer's project.
