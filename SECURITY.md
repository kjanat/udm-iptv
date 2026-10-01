# Security Policy

## Supported Versions

| Version                     | Supported                  |
| --------------------------- | -------------------------- |
| 5.0.0 previews, latest only | yes                        |
| 4.3.x                       | yes                        |
| older 4.x                   | no, run `udm-iptv upgrade` |

A fix for a 5.0.0 preview ships as the next preview. A fix for 4.3 ships as a 4.3 patch release. Both are published as GitHub releases and picked up by `udm-iptv upgrade`.

Releases before 4.0.0 come from the upstream project this repository forks, [fabianishere/udm-iptv](https://github.com/fabianishere/udm-iptv). Report problems in those there.

## Scope

udm-iptv runs as root on a UniFi OS gateway. It configures the WAN VLAN, DHCP, multicast routing, NAT rules and firewall exceptions for IPTV, and installs itself as a Debian package. Anything that lets a network peer, an IPTV stream, a DHCP server or a crafted release influence the gateway beyond that is in scope. So are the telemetry path and the provider lookup at `udm-iptv.kjanat.dev`.

## Reporting a Vulnerability

Email security@kajkowalski.nl, or use GitHub's private vulnerability report form for this repository:

https://github.com/kjanat/udm-iptv/security/advisories/new

Include the firmware version, the udm-iptv version, the provider profile and the steps or packets that demonstrate the problem. Do not open a public issue for it.

You get a first reply within seven days. A confirmed report is fixed and released before any public mention, with an advisory that credits you unless you ask otherwise. A declined report gets the reason.
