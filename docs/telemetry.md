# Telemetry

Reporting helps diagnose failures and improve provider presets.

I do not share this data with ISPs, Ubiquiti, or anyone else. It stays in my Sentry project. I use it to improve provider profiles, to see which firmware builds break IPTV so I can react faster, to find where the CLI or wizard loses people, and to learn which profiles work on which kind of connection so more profiles can be added.

- All reporting categories default on for new configurations, imported legacy files, and saved files that omit a telemetry block.
- The setup wizard offers “Help improve udm-iptv?”.
- Debian installations and legacy migrations use the same reporting defaults. Upgrades preserve saved choices.
- Existing opt-outs remain.
- Reports reach the maintainer's Sentry project through HTTPS.
- Sentry uses an EU ingest endpoint.
- No Sentry account is required.
- Reporting includes identifying data; it is not anonymous.

## Your choices

Use `udm-iptv configure`, or disable all reporting directly:

```sh
udm-iptv configure set --telemetry=false
```

Keep operational reporting, excluding configuration history and network identity:

```sh
udm-iptv configure set --telemetry=true --telemetry-presets=false --telemetry-network-identity=false
```

Individual categories can also be disabled:

| Flag                           | Shared data                                                                   |
| ------------------------------ | ----------------------------------------------------------------------------- |
| `--telemetry-errors`           | Failures, with the operations that led up to them and the failure diagnostics |
| `--telemetry-logs`             | State changes, coalesced warnings and recent subprocess output on failures    |
| `--telemetry-metrics`          | Durations, uptime, restarts and aggregated multicast counters                 |
| `--telemetry-tracing`          | Operation timings, including installation steps and outgoing requests         |
| `--telemetry-presets`          | Selected settings, configuration history and hourly check-in                  |
| `--telemetry-network-identity` | Public IP and reverse-DNS hostname (PTR)                                      |

Network identity requires preset reporting to be enabled.
Individual category flags do not enable the master switch.
A persistent installation ID correlates enabled operational reporting even when preset reporting is disabled.

## Configuration reports

- The complete configuration file: provider profile, WAN interface, VLAN, MAC, DHCP options and route policy, static address and routes, NAT destinations, LAN interfaces, proxy settings and telemetry preferences.
- Software version, git revision, Go toolchain, router model, subsystem id, firmware, firmware discovery string, CPU, OS, Go runtime and the module list of the binary.
- Which settings changed, configuration revisions and saved/applied change timestamps.
- A random installation ID connects configuration history with failures.
- Repeated configuration reports reuse the most recently observed network identity during the lookup interval and retain its observation timestamp. Lookup failures retain their cause.
- Hourly service observations carry uptime, restart count, active state and configuration revision. They do not repeat the configuration. Unhealthy observations and increases in the restart count include a diagnostics snapshot: service and proxy state, interface addresses and routes, per-route multicast counters, bridge memberships, NAT counters and the last DHCP lease. The local service log records counters of removed NAT rules.
- Provider guesses remain separate from your selected provider profile.
- Reports inform preset improvements; presets never change automatically.

## Privacy

Network identity lookup contacts ipify and your DNS resolver.
ipify sees your public IP; DNS resolves its PTR.
Sentry also sees your connection's public IP during reporting.
Disabling network identity removes the IP/PTR fields and lookups.

Automatic telemetry does not attach packet payloads or the files written by `udm-iptv diagnose --capture`. SDK collection of HTTP headers, cookies and request bodies is disabled. Enabled error reports, diagnostic attachments and program logs retain their original text; their content is not redacted.

The maintainer controls Sentry access and retention.
The CLI cannot delete reports already received by Sentry.
Disabling reporting stops new events; queued requests may finish.
Reporting failures do not prevent IPTV operations.

## Reporting priorities

- Failures, panics, lease acquisition/deconfiguration and service readiness retain reporting. Successful DHCP renewals, recoverable DHCP hook callbacks and health checks retain breadcrumbs but do not emit routine logs, metrics or standalone traces. Failed operations still report.
- Identical warnings are sent immediately once, then coalesced for 15 minutes across processes. The next occurrence after that window includes an `occurrences` count covering suppressed repeats and the new occurrence. Distinct warnings have independent windows. The bounded local warning file stores message hashes, timestamps and counts, not message text.
- DHCP, proxy and NAT output remains complete in local output. Remote reporting buffers only the latest 64 KiB per source (at most eight sources) and attaches those bytes to failures when both logs and errors are enabled. Healthy output, including blank lines, consumes no log events. Attachments preserve binary bytes and unterminated lines; older bytes are discarded from the buffer.
- Every five minutes, metrics report uptime, restarts and five multicast totals: routes, unresolved routes, packets, bytes and wrong-interface packets. Per-route metric series are not sent. This is at most 2,016 periodic metric samples per day, independent of route count; operation metrics are additional. Detailed per-route evidence remains in local diagnostics and failure snapshots.
- Healthy hourly observations are compact; detailed snapshots are collected when unhealthy or after a restart increase.

## Delivery diagnostics

Error messages, panic values and diagnostic attachments retain their content. Diagnostic reports larger than 256 KiB use gzip compression. Recent subprocess attachments have the separate tail limits described above.

Reporting remains bounded per category. Existing minute/day budgets are backstops, separate from content selection. Exhausted budgets, inaccessible settings or rate files, queue flush timeouts, transport errors and server rejections do not print automatic delivery warnings to stderr. SDK client reports describe queue and backoff losses, but may remain unsent when a short invocation exits. A successful queue flush is not an acknowledgement from Sentry. Configuration and observation reporting return queue/encoding/flush failures to their callers; automatic configuration reporting is best effort and local configuration history remains intact.

Breadcrumb history retains the newest 50 entries. Sentry Go v0.49.0 reads its span recorder limit from the global SDK hub: the reporter's 32-span client option does not enforce that recorder limit, whose unconfigured global default is 1,000. Exception conversion stops at unwrap depth 16, allowing up to 17 entries on a simple chain; joined errors may have more entries. These limits and deliberate content selection mean remote telemetry is not a complete archive.

## Feedback and identity

Share your experience when preset reporting is enabled:

```sh
udm-iptv telemetry feedback working --provider kpn
udm-iptv telemetry feedback problems
udm-iptv telemetry feedback not-using
```

Reset your reporting identity and local configuration history:

```sh
udm-iptv telemetry reset-id
```

Resetting does not delete previously sent reports.
