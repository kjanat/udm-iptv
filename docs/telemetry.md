# Telemetry

Reporting helps diagnose failures and improve provider presets.

I do not share this data with ISPs, Ubiquiti, or anyone else. It stays in my Sentry project. I use it to improve provider profiles, to see which firmware builds break IPTV so I can react faster, to find where the CLI or wizard loses people, and to learn which profiles work on which kind of connection so more profiles can be added.

- All reporting categories default on for new configurations, imported legacy files, and saved files that omit a telemetry block.
- The setup wizard offers “Help improve udm-iptv?”.
- A fresh Debian installation or legacy migration asks separately before enabling reporting. Unattended package installations leave it disabled unless consent was explicitly preseeded; upgrades preserve saved choices.
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
| `--telemetry-logs`             | Operation logs and the output of the DHCP client and the multicast proxy      |
| `--telemetry-metrics`          | Durations, uptime, restarts and multicast counters                            |
| `--telemetry-tracing`          | Operation timings, including installation steps and outgoing requests         |
| `--telemetry-presets`          | Selected settings, configuration history, installation ID and hourly check-in |
| `--telemetry-network-identity` | Public IP and reverse-DNS hostname (PTR)                                      |

Network identity requires preset reporting to be enabled.
Individual category flags do not enable the master switch.

## Configuration reports

- The complete configuration file: provider profile, WAN interface, VLAN, MAC, DHCP options and route policy, static address and routes, NAT destinations, LAN interfaces, proxy settings and telemetry preferences.
- Software version, git revision, Go toolchain, router model, subsystem id, firmware, firmware discovery string, CPU, OS, Go runtime and the module list of the binary.
- Which settings changed, configuration revisions and saved/applied change timestamps.
- A random installation ID connects configuration history with failures.
- Hourly service observations with a diagnostics snapshot: service and proxy state, the IPTV interface's addresses and routes, the multicast forwarding table with per-route counters, bridge group memberships, every MASQUERADE rule on the IPTV interface with its packet counters, whether each configured NAT destination has a route through that interface, and the last DHCP lease with every option the server sent. The service log records the counters of NAT rules it removes.
- Provider guesses remain separate from your selected provider profile.
- Reports inform preset improvements; presets never change automatically.

## Privacy

Network identity lookup contacts ipify and your DNS resolver.
ipify sees your public IP; DNS resolves its PTR.
Sentry also sees your connection's public IP during reporting.
Disabling network identity removes the IP/PTR fields and lookups.

Never uploaded:

- Credentials.
- Packet payloads or the files written by `udm-iptv diagnose --capture`.

The maintainer controls Sentry access and retention.
The CLI cannot delete reports already received by Sentry.
Disabling reporting stops new events; queued requests may finish.
Reporting failures do not prevent IPTV operations.

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
