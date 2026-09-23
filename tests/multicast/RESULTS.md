# IMProxy querier investigation

## Unmodified implementations

At repository commit `94e04ee1bf315d29be2fa5468b9f627761189095`, native ARM64
experiments demonstrated a downstream querier-election defect in the pinned
upstream source and the executable shipped in both tested firmware packages.

| Experiment                   | Upstream       | UDM Pro 5.1.33 | UCG Max 5.1.33 | CI evidence                                                                |
| ---------------------------- | -------------- | -------------- | -------------- | -------------------------------------------------------------------------- |
| Baseline, 360s               | Pass           | Pass           | Pass           | [35805089532](https://github.com/kjanat/udm-iptv/actions/runs/35805089532) |
| Lower-address querier, 360s  | Election fails | Election fails | Election fails | [35805091220](https://github.com/kjanat/udm-iptv/actions/runs/35805091220) |
| Higher-address querier, 360s | Pass           | Pass           | Pass           | [35805092975](https://github.com/kjanat/udm-iptv/actions/runs/35805092975) |

Upstream source: `haibbo/improxy` commit
`5a9d153d10e19ef835af9fc5f2a971799677a7ed`.
Both firmware packages contain executable SHA-256
`75d7a7a5dc5bc23709c3b11d331ece2d78678a3fc59692a2d50ae870eaa5e822`:
two packaging checks, not two independent implementations.

In the lower-querier runs, valid IGMPv3 General Queries arrived near 10, 41, 166
and 291 seconds. IMProxy logged `unknown igmp type 0x11` and continued its own
General Queries near 33, 158 and 283 seconds. Captures and process logs establish
the receive/ignore/continue sequence. All nine runs passed forwarding and capture
checks; the largest observed UDP interpacket gap was 22.4ms. These runs used an
IGMPv2 receiver and IGMPv3 proxy/query configuration.

This demonstrates the implementation defect. It does **not** establish the cause
of Discussion #365's reported television failure or intermittent channel changes
on another installation. The fixture has no snooping switch, receiver firmware or
UniFi kernel; its exact scope and evidence files are described in [README.md](README.md).

## Fix validation

The [patch](../../patches/improxy/README.md) adds election and recovery with
associated membership-timer handling. Its before/after matrix uses the same
baseline, lower, higher and recovery assertions for unmodified and patched builds,
with IGMPv2 and IGMPv3 separately. CI results must be inspected before concluding
that the patched executable passes; fixture unit tests alone do not establish that.
