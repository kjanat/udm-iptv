# Tasks

## Sentry audit (2026-09-18)

- [x] Observations appear as Issues (`UDM-IPTV-3/4`): fixed in `b180e28`, waiting on a preview release
- [x] `environment: null`: events via `b180e28`, logs via `attributes()`
- [x] Trace context between spans, logs and failures: verified live, trace `057a2b67…`
- [x] No spans in the preview: `DefaultTraceRate = 1`
- [x] `vcs.modified: true`: `/dist/` in `.gitignore`
- [x] Logs without installation ID: present after all
- [x] Breadcrumbs, integrations, contexts, modules: on
- [x] Outgoing HTTP as spans: `sentryhttpclient` around the upgrade and ipify clients
- [x] Proxy and udhcpc output as Sentry logs: `sentryslog`
- [x] Installer steps as child spans
- [x] Failure diagnostics as attachment
- [x] Hourly cron check-in per installation
- [x] Complete configuration in the configuration report, `changed_fields` as JSON paths
- [x] Allowlist and scrubber removed; signed download URLs named without their query
- [x] `docs/telemetry.md` and F1 help updated
- [x] `tool govulncheck` out of `go.mod`; CI runs `go run …@latest`
- [ ] New preview release so the two installations send all of this

## Data gathering without ssh

- [x] `internal/mroute`: full forwarding table with interface names
- [x] Snapshot: per-route entries, unresolved counted separately, addresses, routes with gateway, bridge MDB memberships, raw NAT rules, last lease
- [x] DHCP hook records the lease with every received option (`/run/udm-iptv/lease.json`)
- [x] Hourly observation carries the snapshot
- [x] Per-minute gauges per multicast route
- [ ] UDAPI collector (`ubios-udapi-client` interfaces/services/statistics), todo.md step 1
- [x] `EnsureNAT` reconciles the chain: every MASQUERADE rule on the IPTV interface that is uncommented or not a configured destination is deleted before the `udm-iptv` set is appended

## `diagnose --follow` (PR #38 comment, 2026-09-18 00:28)

- [x] Fixed status block
- [x] Timeline of changes only: routes appearing and disappearing, packets and rate per route, memberships, service/PID/restarts/link/lease
- [x] Packet totals and bytes in the sample lines
- [x] Manual marker (`m`), timestamped in the capture
- [ ] Distinguish IPTV traffic from unrelated multicast (currently: unresolved counted apart, everything else shown)

## Local diagnostics (PR #38 comment, 2026-09-18 00:52)

- [x] No redaction in the local model; every address, prefix and group visible
- [ ] Sanitized sharing report with consistent aliases, deferred until before the next preview

## Port menu (PR #38 comment, 2026-09-17 20:10)

- [x] Ranking by Internet route and WAN candidate
- [ ] Sort: public IP → link status → name → VLAN ID
- [ ] Filter out interfaces without a public IP, or a shortcut for it
- [ ] Stop showing `(no assigned IP, link status unknown)`
- [ ] Remaining commands (`status`, `restart`, `upgrade`, …) as TUI instead of cobra text

## CLI

- [x] `configure set` writes flags without the form, `configure get [name]` prints the configuration or one setting by flag name; `--non-interactive` gone from `configure`
- [x] Consent prompt reworded; `enter/tab` shown in confirm key hints; escape closes help like any key
- [x] `configure get` exits 3 on an unconfigured console; `postinstall` asks it instead of keeping its own list of legacy paths
- [x] Repeated strings are constants; provider IDs and PTR suffixes live in the catalog only

## Cross-cutting audit (2026-09-18, `dba54f3`)

- [x] F01 `Installed` needs the unit file as well as the executable; `configure set` on an unpacked package saves without restarting
- [x] F02 package bootstrap finds `/data/udm-iptv/udm-iptv.conf` through `configure get`
- [x] F03 `--profile custom` keeps the saved interfaces; provider profiles are seeded with the detected ones
- [x] F04 VLAN ownership: created links carry alias `udm-iptv`; an unmarked link is replaced only when it already is the configured VLAN on the configured parent; borrowed interfaces keep their other addresses
- [x] F05 `unpacked`, `half-configured`, `half-installed` and trigger states delegate to apt; `config-files` and `not-installed` do not
- [x] F06 a dpkg-tracked installation upgrades through the attested `.deb` and `apt-get install`
- [x] F07 the capture launcher waits for the worker's first record; exit 0 is a finished capture
- [x] F08 already gone at `9f1ee3f`: no redaction in local captures
- [x] F09 journal entries keep their own timestamp and source; `ubios-udapi-server` lines mentioning udm-iptv are collected too; snapshot labels separate configured policy from observed state
- [x] F10 help calls `0.0.0.0/0` unrestricted; the snapshot says whether improxy applies the source ranges (it does not)
- [x] F11 `.lock` in the state directory serialises install, upgrade, configure and removal; never held across apt
- [x] F12 firmware harness shadows `compopt` and asserts completions; cold named-profile and legacy-backup package installs added (CI only, ARM64)
- [x] R01 attestation identity bound to `release.yml` at the candidate's own tag; token only over https
- [ ] R02 lease application acknowledgement beyond address presence
- [ ] R03 commit boundary for a failed reconfiguration (saved versus applied)
- [ ] Named profile on a saved configuration still reseeds WAN/LAN like the wizard does; decide whether saved hardware choices should survive a profile switch on the CLI

## Done earlier

- [x] 49 review threads on PR #38 resolved
- [x] Diagnostics: unavailable is not zero (`5dcfa4f`)
- [x] 14 stale agent worktrees and 25 merged branches removed
- [x] Tab completion on UniFi OS (`b180e28`)
- [x] `postinstall` called `configure --non-interactive` after that flag was removed in `9f1ee3f`; it calls `configure set` now

## Provider catalog (research index)

- [ ] Init7 `/19` → `/24`, port 5000, IGMPv2
- [ ] Solcon per access network, DHCP option 43
- [ ] Freedom separated from the KPN alias
- [ ] Tweak/Canal Digitaal historical variant
- [ ] Vivo SP lease prefixes, vivogvt two VLAN paths
- [ ] Swisscom option 60, Telenor IGMPv3, BT/MEO/POST/MagentaTV details
- [ ] Six new providers: Proximus, Stipte, Glasnet, Online.nl, Orange FR, Movistar, TELUS
- [ ] Evidence per field in the schema

## Auto-discovery (todo.md implementation order)

- [ ] Step 1 observation collector: kernel side done, UDAPI side not
- [x] Step 2 lease evidence from the hook
- [ ] Step 3 tests with PPPoE labelled static, `iptv` outside UDAPI, multiple WANs
- [ ] Step 4 SAP/IGMP capture for unresolved fields
- [ ] Step 5 fingerprint → catalog score or `custom`
- [ ] Step 6 wizard shows evidence and assumptions
- [ ] Step 7 active VLAN/DHCP trial

## Other

- [ ] Auto-update (timer, channel, policy)
- [ ] `postinstall` Custom branch without automated coverage
- [ ] Unknown stager putting files in the index
- [ ] Working tree: uncommitted changes
