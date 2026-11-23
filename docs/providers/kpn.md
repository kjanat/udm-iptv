# KPN

The KPN profile is based on KPN's technical requirements for customer-provided
equipment. Snapshots are kept for [fibre] and [VDSL].

The specification describes these IPTV requirements:

- routed IPTV on VLAN 4 using DHCP;
- DHCP vendor class identifier `IPTV_RG`;
- DHCP option 121 for provider-specific routes;
- no use of the DHCP-provided default gateway or DNS servers on the IPTV link;
- an IGMP proxy supporting at least IGMPv2 and IGMP snooping on the LAN;
- fast-leave to close unused streams during channel changes; and
- VLAN priority (PCP) 5 on fibre.

The specification does not publish IPTV destination or multicast-source
prefixes. Changes to those profile values therefore require operational
evidence in addition to this document.

[fibre]: ../vendor/kpn/internet-glasvezel-specificaties-2023-01-23.pdf
[VDSL]: ../vendor/kpn/internet-vdsl-specificaties-2023-01-23.pdf
