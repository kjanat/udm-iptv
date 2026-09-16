# Telemetry

Reporting helps diagnose failures and improve provider presets.

- All reporting categories default on for new configurations.
- The setup wizard offers “Help improve udm-iptv?”.
- Existing opt-outs remain; imported legacy configurations default off.
- Reports reach the maintainer's Sentry project through HTTPS.
- Sentry uses an EU ingest endpoint.
- No Sentry account is required.
- Reporting includes identifying data; it is not anonymous.

## Your choices

Use `udm-iptv configure`, or disable all reporting directly:

```console
udm-iptv configure --non-interactive --telemetry=false
```

Keep operational reporting, excluding configuration history and network identity:

```console
udm-iptv configure --non-interactive --telemetry=true --telemetry-presets=false --telemetry-network-identity=false
```

Individual categories can also be disabled:

| Flag                           | Shared data                                                         |
| ------------------------------ | ------------------------------------------------------------------- |
| `--telemetry-errors`           | Error types and filtered call stacks                                |
| `--telemetry-logs`             | Operation starts, completions, failures and cancellations           |
| `--telemetry-metrics`          | Durations, uptime, restarts and multicast counters                  |
| `--telemetry-tracing`          | Sampled operation timings; default sample rate: 10%                 |
| `--telemetry-presets`          | Selected settings, configuration history and random installation ID |
| `--telemetry-network-identity` | Full public IP and reverse-DNS hostname (PTR)                       |

Network identity requires preset reporting to be enabled.
Individual category flags do not enable the master switch.

## Configuration reports

- Software version, router model, firmware and selected provider profile.
- VLAN, DHCP/static mode, proxy, IGMP version and quickleave.
- Default-route policy, debug setting and downstream-interface count.
- Shipped prefixes and custom public IPv4 networks: /8–/24.
- Custom private networks and host routes are excluded.
- Changed settings, configuration revisions and saved/applied change timestamps.
- A random installation ID connects configuration history with failures.
- Hourly service observations help assess stability, not viewer satisfaction.
- Provider guesses remain separate from your selected provider profile.
- Reports inform preset improvements; presets never change automatically.

## Privacy

Network identity lookup contacts ipify and your DNS resolver.
ipify sees your public IP; DNS resolves its PTR.
Sentry also sees your connection's public IP during reporting.
Disabling network identity removes explicit IP/PTR fields and lookups.

Never uploaded:

- Credentials, MAC addresses or complete configuration files.
- Packet payloads, raw errors, proxy logs or diagnostic captures.
- Interface names, configured addresses or arbitrary DHCP arguments.
- Local variables, source excerpts or absolute file paths.

The maintainer controls Sentry access and retention.
The CLI cannot delete reports already received by Sentry.
Disabling reporting stops new events; queued requests may finish.
Reporting failures do not prevent IPTV operations.

## Feedback and identity

Share your experience when preset reporting is enabled:

```console
udm-iptv telemetry feedback working --provider kpn
udm-iptv telemetry feedback problems
udm-iptv telemetry feedback not-using
```

Reset your reporting identity and local configuration history:

```console
udm-iptv telemetry reset-id
```

Resetting does not delete previously sent reports.
