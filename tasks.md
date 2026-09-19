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
- [x] New preview release so the two installations send all of this, ensure you ASAP modify the release notes with info ppls need2know!\
      `v5.0.0-preview.2`: notes open with a "not for your router yet" block, prerelease flag set, `releases/latest` stays `v4.3.1`.\
      Prevent ppls from installing preview versions that are either intended for own testing only, or send ALL telemetry to sentry.\
      The latter has not occured, and I'd like to keep it that way.

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
- [x] F03 switching profile keeps the console's WAN port and LAN bridges; only `--profile custom` on a fresh console starts from the detected ones (`a4f54f1`)
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
- [x] R02 the hook records whether it applied the lease; the daemon waits for this run's applied record on its interface and fails on a recorded failure instead of an address
- [x] R03 a configuration the service will not start with is moved to `config.json.rejected`, the previous file is restored and the service restarted on it; the report still says what was attempted and that it did not apply
- [x] Named profile on a saved configuration reseeded WAN/LAN; saved hardware choices survive a profile switch on the CLI and in the wizard (`a4f54f1`)

## Done earlier

- [x] 49 review threads on PR #38 resolved
- [x] Diagnostics: unavailable is not zero (`5dcfa4f`)
- [x] 14 stale agent worktrees and 25 merged branches removed
- [x] Tab completion on UniFi OS (`b180e28`)
- [x] `postinstall` called `configure --non-interactive` after that flag was removed in `9f1ee3f`; it calls `configure set` now
- [x] preview.1 → preview.2 on the router swapped the executable behind dpkg, leaving `5.0.0~preview.1` recorded. `upgrade` now compares the candidate with dpkg's record as well as the running binary and reinstalls the package when they disagree, without `--force`; `status` prints `Installation: package …` and names a stale record
- [x] `templates` used the po-debconf `_Description` field, so every install printed `debconf: Unknown template field '_description'`; it is `Description` now

## NAT evidence (router check 2026-09-19, preview.1 on the UDM-Pro)

Found with `iptables -t nat -L POSTROUTING -v -n -x`, `ip -4 route show dev iptv`, `ip mroute`:

- Six MASQUERADE rules on `iptv`: three legacy ones without comment ahead of the three `udm-iptv` ones. Only legacy `213.75.0.0/16` has matched (187 packets in ~30 h); every `udm-iptv` rule shows 0, so the hourly observation has been sending zeros. `f4b5ef9` reconciles this on the next install/restart; needs the preview release to reach the router.
- `217.166.0.0/16` and `195.121.0.0/16` can never match: the only route via `iptv` is the option-121 `213.75.112.0/21`; traffic to the other two leaves via `ppp0`. A NAT rule on `-o iptv` for a destination without a route via `iptv` is dead. NAT `0.0.0.0/0` + `no-default` is therefore exactly "NAT the lease-routed destinations".
- `195.121.94.212` is the SAP announcer (group `224.0.250.64`, mroute iif `iptv` → `br0`, ~198k packets): a multicast source, never a unicast NAT target.
- Snapshot keeps only rules containing `udm-iptv` (`managedNATRules`, `ListWithCounters`), so the shadowing is invisible in Sentry. Counters reset whenever rules are re-appended.

Proposal, agreed 2026-09-19:

- [x] Snapshot keeps every MASQUERADE rule on the IPTV interface, marked managed/unmanaged, with counters (`network.ListNAT`, `natRules` in the observation)
- [x] Per-destination evidence derived from the route list (`routed (…)` / `no route via iptv`, packets, unmanaged rules for the same destination), in `status`, `diagnose`, and as `natEvidence` in the observation
- [x] Counters logged when reconciliation or shutdown removes a rule (`NAT rule removed: …` to the journal and the Sentry log). A rule that stays keeps its counters; a `0.0.0.0/0` rule is now recognised in the form iptables prints it, so it stays too
- [x] Preview release so the router runs the reconciliation: `v5.0.0-preview.2` on `f189e2b`, published 2026-09-19; router upgraded the same night, legacy rules removed (224 packets on `213.75.0.0/16` at removal), removal lines and counters confirmed in Sentry logs

## Provider catalog (research index)

Operator pages fetched 2026-09-19. Init7 pages are JavaScript-rendered (WebFetch sees "Loading..."); open them in a browser. Proximus PDF extracted to text in the scratchpad (`pdftotext`), source URL in `.archive/udm-iptv-provider-sources.md`.

- [ ] Init7 `/19` → `/24`, port 5000, IGMPv2 (page content still to read)
- [ ] Solcon split: `solcon` VLAN 4 (Solcon Operator PON, KPN FTTH/Opticks, KT-Waalre, KPN xDSL) and a new profile for CAIW-EAS/Delta VLAN 188. General spec: IGMPv2 proxy, option 60 = `IPTV_RG`, options 15/42/43/121 returned, 43 must be relayed WAN → LAN
- [ ] Freedom separated from the KPN alias: helpdesk says DHCP on VLAN 4, NAT on, IGMP snooping/proxy on VLAN 4, RTSP conntrack; TV is CANAL+ (Amino A710); no VCI or prefixes published → NAT `0.0.0.0/0`, sources empty
- [ ] Stipte: AON internet VLAN 2 / TV VLAN 4, PON internet 970 / TV 168, both DHCP, IGMPv2, one fixed /32; bridging recommended, routed mode unsupported by them; page updated 2025-03-05
- [ ] Online.nl: VDSL and KPN fibre VLAN 4, DFN VLAN 248, IPoE/DHCP, NAT on, IGMP proxy, option 121; no VCI or prefixes published
- [ ] Glasnet: DELTA and ODF both TV VLAN 37 DHCP, IGMP proxy+snooping, policy route + forced NAT to `185.24.175.211/32` (Canal Digitaal/M7 platform), RTSP ALG 554; route needs the lease gateway, which `wan.staticRoutes` cannot express
- [ ] Proximus: VLAN 20 is the shared residential WAN (needs the existing-WAN mode, not a second DHCP client); reachable `172.28.40.0/21`, `172.28.48.0/21`; groups `239.192.0.0/16`, `239.255.0.0/16`; options 6/42/43/67 relayed to the decoder; option 55 must request 1,3,6,12,15,42,43,51,54,67,121; IGMPv3 snooping preferred
- [ ] Tweak/Canal Digitaal historical variant, Vivo SP lease prefixes, vivogvt two VLAN paths, Swisscom option 60, Telenor IGMPv3, BT/MEO/POST/MagentaTV: documentation only, no operator-published narrower ranges
- [ ] Schema: `sourceRanges` description still says multicast groups belong there; `validateProxy` rejects them (`errGroupAsProxySource`). Allow an empty `sourceRanges` (= unknown; the wizard already forces igmpproxy users to fill it in). `natDestinations` stays required
- [ ] `docs/providers/<id>.md` per touched provider with source URL and date, same pattern as `kpn.md`; `TestCatalogNavigation` counts 5 NL providers
- [ ] Profiles cannot set the IGMP version (Init7, Stipte, Solcon say v2); document instead

## Router dumps (`.archive/`, same router as `ssh router`)

- `ssh router` has `RemoteCommand` in `~/.ssh/config`; run commands with `-o RemoteCommand=none -o RequestTTY=no`
- `udapi-config.zip` = `/data/udapi-config`: `udapi-net-cfg.json` (config tree, top-level keys `interfaces`, `services`, `firewall/*`, `routes/static`, …) and `ubios-udapi-server.state`. `GET /interfaces` fields verified live: `identification.{id,type,mac}`, `vlan.{id,interface.id,egressQoSMap}`, `pppoe.{interface.id,username,password}` (secret), `addresses[].{cidr,origin,type,inUse}`, `status.{plugged,wanStatus,statistics.*}`. `iptv` is absent. `services.igmpProxy` is null, `wanFailover` names `ppp0` table 201
- `ubios-udapi-server/udhcpc-{ip,action}.eth7.4` (2023-12-18, `bound`, `10.207.104.5`): UDAPI itself once ran udhcpc on a VLAN-4 subinterface, so a UDAPI-managed IPTV VLAN is representable
- `udm-iptv.zip` = `/data/udm-iptv` on preview.1: `config.json` still carries `allowDefaultRoute: true`; `/run/udm-iptv/lease.json` does not exist on preview.1
- `log.zip` = `/var/log` (daemon.log, messages, kern.log, ppp0.log, wan-diag-*); `ppp.zip` = `/etc/ppp` with secrets; neither read yet
- `Per_issue.txt`, `Samenvatting_feiten.md`, `preview1.md`: v4-era notes; `Alles_gevonden…`, `Stand_van_zaken.md`: unrelated (Zed)

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
