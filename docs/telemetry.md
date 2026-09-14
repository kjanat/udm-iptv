# Optional telemetry

Telemetry is **off by default**, including when migrating an existing installation.
No Sentry account is needed. When enabled, reports go to the maintainer's Sentry
project through its EU ingest endpoint. The receiver can see the connection's
public source IP; this is not anonymous reporting.

## Enable or disable

Use `udm-iptv configure` to choose which products to enable, or:

```console
udm-iptv configure --non-interactive --telemetry=true
udm-iptv configure --non-interactive --telemetry-logs=false
udm-iptv configure --non-interactive --telemetry-trace-rate=1
udm-iptv configure --non-interactive --telemetry=false
```

The first command enables the saved product selection (all four on a new
configuration). Each product also has a separate flag: `--telemetry-errors`,
`--telemetry-logs`, `--telemetry-metrics`, and `--telemetry-tracing`.
Selecting products does not enable the master switch. Tracing defaults to 10%
of operations; a rate of `1` selects all operations, subject to reporting limits.

Configuration changes restart an installed service. Disabling telemetry prevents
new events from being queued, including by an old daemon that has not stopped yet.
Previously queued or in-flight requests may finish; disabling does not delete
reports already received by Sentry.

## What is sent

- **Errors:** failed managed operations and panics caught on their executing
  goroutine, with error type and a stack at the reporting point. Raw error and
  panic text stays local. These stacks do not necessarily identify where a
  returned Go error was originally created. Panics in unrelated goroutines and
  forced process termination are not automatically captured.
- **Logs:** fixed lifecycle messages such as `dhcp.renew completed`. These are
  explicitly instrumented operations, not forwarded stdout, proxy output or journals.
- **Metrics:** operation completion/failure counts and durations; once a minute,
  daemon uptime, systemd restart count and host-wide multicast route/packet totals.
  Multicast counters can reset and may not reflect hardware-offloaded traffic.
  They do not establish whether a receiver has a picture.
- **Tracing:** sampled operation durations, including DHCP acquisition and service
  health checks. The daemon's entire lifetime is not one long transaction.

Metadata is limited to the software release, known model/profile/proxy names and
a numeric firmware version when available. Unknown values are omitted.
No raw network addresses, MAC addresses, hostnames, user identifiers, credentials,
configuration files, packet payloads, command arguments or diagnostic captures are
attached. Stack frames retain source basenames, functions and line numbers;
absolute paths, source excerpts and local variables are removed.

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

Diagnostic captures remain local and are never uploaded by this integration.
Tests use a recording transport; running the test suite does not send test events
to the maintainer's project.
