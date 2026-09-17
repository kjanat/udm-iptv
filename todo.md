# Auto-discovery

Goal: derive a candidate `Config` from evidence on the router, without requiring the user to choose a provider. Matching against the catalog is optional. No match → `profile: custom` once required fields are resolved. Discovery alone does not establish that TV works.

Keep an observation record separate from `Config`: value, interface, evidence source, observation time/window, confidence, and unresolved fields. Distinguish direct observations, inferred settings, catalog assumptions, and user overrides. Missing evidence stays unknown; confirm assumptions before applying the candidate.

Already implemented: WAN port (`ethN` via default route/sysfs), LAN bridges (`br*`), PTR hint for the brand name (opt-in, no VLAN/DHCP/SAP).

`diagnose` counts mroute entries and NAT strings. DHCP prefixes and multicast sources are not covered yet.

Discovery starts by reading UniFi's existing state and the kernel. Reuse existing topology, health monitoring, memberships, routes, and lease evidence before adding packet capture or active trials.

## Verified local sources (UDM-Pro, firmware 5.1.31)

Read-only queries on the running gateway established the following. These are observations from one device and firmware version; detect capabilities and preserve fallback behavior on other versions.

| Source                                                                         | Verified information                                                                                               | Limit                                                                                                 |
| ------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------- |
| `ubios-udapi-client -r GET /interfaces`                                        | Interface types, PPPoE and VLAN parents, VLAN IDs/QoS maps, bridge members, link status                            | The active `iptv` interface was absent; address labels do not establish allocation policy             |
| `ubios-udapi-client -r GET /services`                                          | `wanFailover` active/primary state, routing tables, existing health-monitor results, `igmpSnooping`                | Query the service collection and select fields; `/services/wanFailover` returned 404 on this firmware |
| `ubios-udapi-client -r GET /statistics`                                        | Interface traffic, multicast, error and drop counters                                                              | The active `iptv` interface was absent here too                                                       |
| `ubios-udapi-client -r INTERNAL /internal/interfaces/runtime`                  | Interface identifiers and `runtimeStatus.blocked` lists                                                            | Did not expose received DHCP options or address-allocation details in this inspection                 |
| Linux link/address/route state (`ip -d -j link`, `ip -j -4 route`, or netlink) | Includes interfaces outside UDAPI's inventory; `iptv` was VLAN 4 on `eth8`, with a DHCP route to `213.75.112.0/21` | Installed routes do not reproduce the complete original lease                                         |
| `bridge -j mdb show`                                                           | Existing multicast group/port memberships; 11 entries at inspection time                                           | Membership is not proof of successful TV playback or complete channel coverage                        |
| `ip -j -4 mroute show`                                                         | Source/group/interface state; five entries at inspection time                                                      | Check forwarding counters separately; entries alone do not establish current reception                |

UniFi explicitly reported `ppp0` as the active WAN, routing table 201, and seven existing health monitors. Its interface relationships gave `ppp0` → `eth8.6` → `eth8`. Reuse these observations instead of running duplicate internet-health probes or guessing the parent chain. Reconcile UDAPI's managed topology with kernel state so project-managed links remain visible.

The CLI talks to a local Unix socket. Keep access read-only, bounded by timeouts and output limits, and parse only required fields: interface responses can include PPPoE credentials. Export sanitized observations rather than full API responses. Treat missing endpoints, malformed responses, and absent fields as unavailable evidence. The local Network application API remains a possible additional source; its field coverage has not been verified here.

## IPv4 address-label semantics in 5.1.31

The supplied reverse-engineering trace of the exact [5.1.31 firmware] establishes that the IPv4 runtime path calls `rtnl_addr_get_flags()` and tests [IFA_F_PERMANENT] (`0x80`): set → `addresses[].type = static`, clear → `dynamic`. The relevant instructions were identified at `0x5ce0bc` and `0x5ce1f0`, with enum-to-string mapping in `libudapi.so`. The firmware also validates that PPPoE does not support its IPv4 dynamic-address mode.

This describes kernel address classification. It does not establish manual configuration, an ISP-reserved address, or persistence across PPP sessions. The live response identified `ppp0` as `pppoe` while labeling its IPv4 address `static`, consistent with that trace. Preserve `identification.type` and parent relationships as connection-mechanism evidence. Never derive `wan.staticAddress` from this label; require independently confirmed static configuration. IPv6 has additional classification logic and is outside this finding.

---

### 1. Fields

| Field                 | Role                                     | Can it be measured?                                                           |
| --------------------- | ---------------------------------------- | ----------------------------------------------------------------------------- |
| `wan.interface`       | physical uplink                          | UDAPI parent relationships plus kernel topology                               |
| `wan.vlan`            | 802.1Q path                              | existing UDAPI/kernel VLAN state first; unresolved candidates may need trials |
| `wan.vlanInterface`   | local name (`iptv`)                      | default, not ISP-specific                                                     |
| `wan.vlanMAC`         | spoofed ISP box MAC                      | only if required by the ISP; cannot be guessed                                |
| `wan.dhcp`            | lease vs static                          | DHCPACK or existing lease evidence; timeout leaves mode unknown               |
| `wan.dhcpOptions`     | udhcpc (`-O staticroutes`, `-V IPTV_RG`) | existing client configuration first; distinguish sent options from responses  |
| `wan.dhcpRoutes`      | default route via the IPTV path          | policy decision informed by advertised routes and existing connectivity       |
| `wan.staticAddress`   | static address                           | requires confirmed static configuration; an assigned address is insufficient  |
| `wan.natDestinations` | MASQUERADE `-d`                          | route candidates from option 121; NAT need requires separate evidence         |
| `wan.staticRoutes`    | additional unicast routes                | yes, same lease                                                               |
| `lan.interfaces`      | where the TV is connected                | yes, `br*` + IGMP joins                                                       |
| `proxy.program`       | improxy / igmpproxy                      | local, not ISP-specific                                                       |
| `proxy.igmpVersion`   | 2 or 3                                   | per-interface compatibility evidence, subject to proxy capabilities           |
| `proxy.quickLeave`    | local                                    | no (multiple boxes)                                                           |
| `proxy.debug`         | local                                    | no                                                                            |
| `proxy.sourceRanges`  | igmpproxy `altnet`                       | exact observed/advertised sources; wider ranges require justification         |
| `profile`             | catalog ID                               | fingerprint after measurement                                                 |
| PCP 5 (KPN fiber)     | 802.1p                                   | absent from config, present in the KPN specification                          |

---

### 2. Layers

#### A. Passive (create nothing)

Runs when the IPTV interface or WAN already exists, or after a previous installation. Read existing state first; capture only for a named unresolved question.

- UDAPI interface relationships, WAN selection, routing tables, and existing health results. Reconcile with kernel links/carrier and retain the default-route/sysfs walk as a fallback (`ppp0` → `eth8.6` → `eth8`).
- Existing VLAN subinterfaces on that WAN (`eth8.6` = internet, do not take it over).
- `ip -4 addr` / `proto dhcp` routes on `iptv` or a VLAN subinterface.
- Existing bridge MDB memberships and multicast routes via netlink or `bridge -j mdb show` / `ip -j -4 mroute show`; `/proc/net/ip_mr_cache` as a fallback (source, group, iif).
- iptables NAT counters on `-o iptv` (first packet per conntrack entry, not traffic volume).
- If existing state leaves IGMP version, querier, or session information unresolved, use an initial 30s multicast capture: queries/reports and any SAP announcements already delivered to the interface. Record the window; absence is inconclusive. SAP destinations depend on scope ([RFC 2974]); treat `224.0.250.64:9875` only as a provider-specific candidate requiring evidence.
- Bridge snooping / querier on `br*` (already in diagnose-downstream).
- PTR of the public IP (implemented, opt-in). Brand name only.

SAP availability and delivery depend on the network; do not assume it exists or reaches the interface without membership. Joining a group belongs in the active phase. RFC 2974 uses a minimum base announcement interval of 300s, with randomization, so 30s cannot establish absence. Record announcers separately from advertised sessions and observed video sources. Without source evidence, leave source discovery unresolved.

#### B. Active (short trial, then clean up if it fails)

Build candidates from passive evidence first. Active trials are a later fallback for unresolved service discovery, such as an unconfigured IPTV VLAN, after existing API/kernel/lease sources have been exhausted. No native UniFi discovery API for that case has been established. Try the combinations below only where existing interface ownership permits. A successful DHCP exchange identifies an address service, not IPTV. Require additional IPTV evidence, such as a relevant session announcement or observed stream, and user confirmation when identification remains uncertain. Do not stop at the first lease.

1. Untagged DHCP on WAN, without a vendor class.
2. Untagged DHCP, `-O staticroutes`.
3. Tagged VLAN IDs from the catalog + common IPTV IDs, each:
   - without VCI
   - with `-V IPTV_RG` (KPN/XS4ALL/Freedom/Solcon)
   - with `-O staticroutes` only (Tweak)
4. VLAN IDs already present as subinterfaces on the WAN, except the internet path (`eth8.6` / PPPoE parent).
5. No DHCP → inspect existing IPv4 configuration and its owner (including PPP on `ppp0`). An address alone does not identify static configuration or IPTV.

Initial trial timeout ~30s per combination; timeouts are inconclusive. Do not run a competing DHCP client on an interface already managed by UniFi or another process. Collect lease evidence through a discovery-specific hook without applying it to the live configuration. Any connectivity validation requires isolated routing and explicit ownership of trial resources.

Preserve existing addresses, DHCP clients, routes, policy rules, DNS, and interfaces. Clean up only trial-owned state on success, failure, cancellation, and timeout; retain a successful result as evidence for a later explicit apply step. Never install a trial default route into live routing, whether advertised in option 3 or option 121, and prevent more-specific trial routes from redirecting existing traffic.

Implementation prerequisite: `wan.dhcpRoutes` decides whether a lease may install a default route, independently of the option that advertised it. `no-default` installs the advertised RFC3442 routes but drops a `0.0.0.0/0` among them and adds no Router-option default; `allow-default` installs a default route from either option; `none` reproduces udm-iptvd's `NO_GATEWAY`, which suppressed the specific routes too. Discovery must never install a trial route into live routing whichever policy applies, and the existing lease hook must not be reused as trial isolation.

Current catalog VLAN IDs: `0, 4, 20, 35, 4000`.

#### C. Fingerprint

After collecting evidence from a trial or existing link:

1. Keep exact advertised route prefixes, SAP announcers, advertised source information, and observed multicast sources as distinct evidence. Do not expand hosts to /16 or /8.
2. VLAN ID + address-assignment evidence (DHCP/static/PPP/unknown) + VCI sent. A successful trial with VCI does not prove VCI is required.
3. Score against `profiles.json`, keeping route candidates separate from NAT policy and announcers separate from stream sources. Record which fields support or contradict a match; do not use inferred catalog values to inflate the score.
4. High score → suggest that profile for the user to confirm. Any additional catalog settings remain labeled assumptions.
5. No match → a `custom` candidate with supported settings and explicit unresolved fields. Resolve required fields before applying. Do not invent `0.0.0.0/0`.

The PTR hint and fingerprint may disagree. Evidence from the wire wins.

---

### 3. Per signal, in theory

#### WAN port

The default-route/sysfs walk is already implemented. Prefer explicit UDAPI WAN selection and PPPoE/VLAN parent relationships where available, corroborated by kernel state; retain the walk as a fallback. Dual-WAN: use the board table only as an ordering hint.

The Magenta profile uses both internet and TV on `ppp0`, with `vlan: 0` meaning no additional VLAN configured here. A default route and relevant multicast/SAP on `ppp0` support a shared-uplink candidate; they do not uniquely identify Magenta. Preserve PPP ownership of the address and link.

PostTV sets `interface: eth8.35` (VLAN in the interface name, `vlan: 0` in the profile). Discovery must choose `eth8` + VLAN 35 or the existing subinterface, not blindly hardcode `eth8.35`.

#### VLAN ID

There is no DHCP option for this. 802.1Q is L2.

Options:

- Existing VLAN IDs and parent relationships from UDAPI and the kernel; identify and preserve the internet path.
- 802.1Q tags in frames on the raw WAN (promiscuous mode, short capture). Detects tags without creating an interface. Misses silent VLANs with no traffic.
- Provider documentation / catalog as a final hint, not as ground truth.
- Trial (layer B) only when existing evidence cannot resolve a candidate.

#### DHCP vs static

A DHCPOFFER is preliminary; DHCPACK establishes a committed lease ([RFC 2131]). A completed exchange supports DHCP availability on that candidate, but does not identify IPTV.
No response in any trial → address-assignment mode remains unknown. An existing IPv4 address may come from a lease, PPP, or static configuration. Inspect its owner and lease/configuration evidence before choosing `dhcp` or `staticAddress`; never freeze a dynamic address into static configuration.
No confirmed assignment method or address → ask the user. BT (`10.20.30.1/24`), Vivo GVT (`10.0.0.1/32`), PostTV (`10.10.10.10/32`) are catalog examples, not discoverable defaults.

For UDAPI IPv4 runtime addresses in 5.1.31, `type: static` reflects `IFA_F_PERMANENT`; it must not select static addressing. A PPPoE-owned link stays PPPoE-owned regardless of that label.

#### DHCP options

[RFC 2132]/[RFC 3442]:

| Option                       | What it tells us                                                                   |
| ---------------------------- | ---------------------------------------------------------------------------------- |
| 1 subnet                     | prefix length of the IPTV transport (`10.207.64.0/20`)                             |
| 3 router                     | default gateway on the TV path. Usually do **not** install it.                     |
| 6 DNS                        | Vivo lists 177.16.30.67/7. Evidence, not a config field.                           |
| 12 hostname / 15 domain name | sometimes an ISP hint. Weak signal.                                                |
| 43 vendor encapsulated       | sometimes IPTV parameters. ISP-specific parser, no universal RFC content.          |
| 60 VCI                       | sent by the client (`IPTV_RG`). Trial, not read from the server.                   |
| 121 classless static routes  | destination/gateway pairs, possibly a default; no NAT requirement encoded.         |
| 249                          | compatibility route option where supported; record separately from 121.            |
| other options                | interpret by assigned semantics; do not assume all codes 240+ are vendor-specific. |

udhcpc `-O staticroutes` explicitly requests 121. Record the actual request and response; absence of this flag alone does not prove that 121 cannot be returned. Preserve advertised routes separately from installed routes, which may have been filtered or supplemented locally. Missing route evidence stays unresolved; SAP/mroute sources cannot substitute for unicast NAT destinations.

UDAPI exposes configured `ipv4.dhcpOptions`; the inspection did not establish an endpoint for received IPTV lease options. The project-managed `iptv` link was absent from UDAPI. Retain structured evidence from the project's existing DHCP hook on normal lease events rather than starting a second client to recover it. Until that evidence is available, use installed routes with their provenance and leave missing lease fields unknown.

#### NAT destinations

Option 121 supplies route candidates, not NAT requirements ([RFC 3442]). Prefer the captured lease as evidence of what the server advertised. Installed `proto dhcp` routes are useful corroboration, but may include locally generated gateway routes or omit rejected routes.

Options after that:

- Keep exact advertised prefixes (`213.75.112.0/21`) in the observation record. Separate defaults and link/gateway routes from candidate service destinations.
- Generate NAT rules only with supporting deployment evidence, an explicitly accepted catalog policy, or a user override. Keep uncertainty visible.
- A broader catalog range remains an assumption requiring confirmation; prefix containment alone does not justify expanding NAT coverage.
- RIPE/whois origin AS of those prefixes (network access, privacy, slow). Diagnostics only, not required for TV.

Multicast groups do **not** belong here (`-d` matches the destination; a SAP source is a source).

#### Proxy sources

igmpproxy filters by **source**. improxy does not (`altnet` does not exist there).

Options:

- SAP announcers and any advertised source information, recorded separately. The announcer need not be a video sender; multicast destination groups are not unicast source ranges.
- IGMP general query source (querier). May be [RFC 1918] (for example `10.60.150.13`) and outside service-destination prefixes. Record it as a control-plane source; determine any altnet requirement from the proxy's behavior, separately from video sources.
- `ip_mr_cache` source column when someone is watching, retaining interface/group context and checking that packets are actually flowing.
- IGMPv3 INCLUDE lists from the STB as requested sources; reception remains unverified until traffic is observed.
- No source evidence → leave unused sourceRanges empty for improxy. For igmpproxy, require sufficient evidence or an explicitly confirmed source policy before applying. Never silently substitute `0.0.0.0/0`, and do not make SAP the only acceptable evidence.

Retain exact observed hosts (/32) and explicitly advertised prefixes. Broader catalog ranges require independent justification and confirmation. A short capture may miss sources used by other channels; present that coverage limit. Privacy redaction in reports must not widen operational source ranges.

#### IGMP version

Record queries and reports separately per interface, including older-version queriers and membership evidence. Apply compatibility rules and the selected proxy's capabilities ([RFC 3376]); do not take the highest version across WAN and LAN. The current config exposes one version setting, so report incompatible or unresolved observations instead of silently collapsing them. If nothing is observed, 3 is a labeled local default, not a discovered fact.

#### LAN

Use UDAPI bridge membership plus kernel bridge/address state. Existing MDB entries identify group memberships and ports without a capture; distinguish IPTV evidence from unrelated multicast. Capture IGMP joins only if the existing state leaves the viewing network unresolved. Unchecked bridges do not get TV.

#### MAC spoofing

Cannot be discovered. Only fill in if the user supplies the box MAC, or if DHCP fails without spoofing but succeeds with the UniFi WAN MAC or a requested MAC (second trial, rare).

#### Proxy binary, quickleave, debug, telemetry

No ISP signal. Keep defaults (`improxy`, quickleave off, debug off).

---

### 4. What “complete” does not mean

- Treating a playbook from one connection (n=1) as a nationwide truth. The fingerprint may say KPN. Ranges remain those measured.
- Guessing the VLAN from PTR (`kpn.net` → 4). PTR identifies the brand; VLAN is L2.
- `0.0.0.0/0` as an inferred source/NAT policy or automatic fallback. An advertised default route can be recorded as evidence without installing it.
- Whois/email to the ISP as a runtime path.
- PCP 5, unless we add it as an extra `ip link` option.
- Static-only providers with no address on the wire.
- Confirming TV playback. That remains “not checked”.

---

### 5. Implementation order if this ever becomes code

1. Add a read-only observation collector to `diagnose`: UDAPI interfaces/services/statistics plus existing kernel links, routes, bridge MDB, multicast routes, and NAT counters. Include PPPoE/shared uplinks and project-managed links from the start. Store provenance, confidence, timestamps, and unknowns. Reuse existing health-monitor results. Redact exported reports separately from exact local operational evidence.
2. Retain structured lease evidence from the existing DHCP hook's normal events. Distinguish configured requests, received options, and installed routes; do not launch duplicate DHCP clients.
3. Add focused tests using sanitized observations: PPPoE labeled static, `iptv` absent from UDAPI but present in Linux, multiple WANs, unavailable/malformed UDAPI responses, and missing lease evidence. The collector must leave router configuration unchanged and export no credentials.
4. Add bounded SAP/IGMP capture only for fields still unresolved by existing state. Record incomplete coverage explicitly.
5. Fingerprint function: observations → catalog scores or a `custom` candidate, with assumptions and unresolved fields preserved.
6. Wizard: present evidence and assumptions for confirmation, with overrides and required-field resolution before apply.
7. Active VLAN/DHCP trial only for remaining discovery gaps during initial setup, after fixing option-121 default-route handling and validating trial isolation, cancellation, cleanup, and preservation of existing network state.

Step 1 is already useful without changing the wizard (range discussions like issue kjanat/udm-iptv#30).

<!-- link definitions -->

[5.1.31 firmware]: https://fw-download.ubnt.com/data/unifi-dream/f100-UDMPRO-5.1.31-c840591d-ddc5-4ab4-a08b-4df62f47403d.bin "f100-UDMPRO-5.1.31.bin (2026-09-17T14:50:39Z)"
[IFA_F_PERMANENT]: https://github.com/torvalds/linux/blob/238650ef6c7c7cca08e032527329424c9fbd70e5/include/uapi/linux/if_addr.h "include/uapi/linux/if_addr.h (2026-09-17T14:50:39Z)"
[RFC 1918]: https://www.rfc-editor.org/info/rfc1918/ "BCP 5: Address Allocation for Private Internets (This RFC was updated, see RFC 6761.)"
[RFC 2131]: https://www.rfc-editor.org/info/rfc2131/ "Dynamic Host Configuration Protocol"
[RFC 2132]: https://www.rfc-editor.org/info/rfc2132/ "DHCP Options and BOOTP Vendor Extensions (This RFC was updated, see RFC 3442, RFC 3942, RFC 4361, RFC 4833, RFC 5494.)"
[RFC 2974]: https://www.rfc-editor.org/info/rfc2974/ "Session Announcement Protocol"
[RFC 3376]: https://www.rfc-editor.org/info/rfc3376/ "Internet Group Management Protocol, Version 3"
[RFC 3442]: https://www.rfc-editor.org/info/rfc3442/ "The Classless Static Route Option for Dynamic Host Configuration Protocol (DHCP) version 4"
[RFC 3942]: https://www.rfc-editor.org/info/rfc3942/ "Reclassifying Dynamic Host Configuration Protocol version 4 (DHCPv4) Options"
[RFC 4361]: https://www.rfc-editor.org/info/rfc4361/ "Node-specific Client Identifiers for Dynamic Host Configuration Protocol Version Four (DHCPv4)"
[RFC 4833]: https://www.rfc-editor.org/info/rfc4833/ "Timezone Options for DHCP"
[RFC 5494]: https://www.rfc-editor.org/info/rfc5494/ "IANA Allocation Guidelines for the Address Resolution Protocol (ARP)"
[RFC 6761]: https://www.rfc-editor.org/info/rfc6761/ "Special-Use Domain Names"
