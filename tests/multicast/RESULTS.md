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

At commit `22adcc0d2f209239f07c17f83c526043ac408997`, the same eight-case ARM64
matrix ran against original source and the [patch](../../patches/improxy/README.md).
Each scenario ran for 360 seconds, after its own five-second smoke test. Proxy,
receiver and second-querier versions were all set to the version in the table.

| Scenario       | Original v2 | Patched v2 | Original v3 | Patched v3 |
| -------------- | ----------- | ---------- | ----------- | ---------- |
| Baseline       | Pass        | Pass       | Pass        | Pass       |
| Lower querier  | Fail        | Pass       | Fail        | Pass       |
| Higher querier | Pass        | Pass       | Pass        | Pass       |
| Recovery       | Fail        | Pass       | Fail        | Pass       |

[Red CI](https://github.com/kjanat/udm-iptv/actions/runs/35809809077) retains the
four actual election failures;
[green CI](https://github.com/kjanat/udm-iptv/actions/runs/35809811873) passes all
eight cases. Neither run treats a known defect as an expected-success result.
[Automatic smoke](https://github.com/kjanat/udm-iptv/actions/runs/35809801397)
also passes for original source, patched source and both firmware packages.

The artifact audit reparsed every LAN/WAN PCAP and compared it with the recorded
IGMP events, checked each run's file hashes and namespace cleanup, and verified
native ARM64 provenance. Both runs used one consistent executable hash across
their eight cases. The exported patched ELF matches the tested executable:
`9f37d6bbeac26dbe5cad1461ee92d2e9d0247dac393fa60d756a39fff8304d7f`.
It is statically linked; the upstream executable retains its original hash
`06d06def658c06c9f42c55bf4682b1b361111bbbc7374919f372f42062a22d7a`.

With a continuing lower querier, patched IMProxy sends only its initial General
Query. In recovery, the lower querier sends at approximately 10 and 41.3 seconds,
then becomes silent. Patched IMProxy resumes **255.098293s (v2)** and
**255.074034s (v3)** after the last query. Thus the second query refreshes the
deadline, and the proxy does not remain permanently passive.

All sixteen scenarios pass forwarding/capture checks and report no observed
sequence gaps between received packets. The largest UDP interpacket gap was
22.43ms. These facts establish the bounded protocol fix, not the cause of the
reporter's television failure. No router executable was replaced.

The first patched artifact omitted `patch-sources/.gitignore` and `.dockerignore`
because GitHub's upload action excludes hidden files by default. Its binary,
patch, source archive and captures were present and hash-verified; the workflow
now includes hidden files within the dedicated evidence directory so the exported
bundle can satisfy its entire checksum manifest.

The [follow-up run at `e29e973`](https://github.com/kjanat/udm-iptv/actions/runs/35810519259)
passed all eight scenarios with unchanged protocol code and tests. Its complete
artifact audit verified **333 files**, including both hidden files, reparsed every
capture, confirmed the configured query/report versions on the wire, and matched
the exported binary to the same SHA-256 above. Recovery occurred after
255.030793s (v2) and 255.006423s (v3); the largest observed UDP gap was 23.60ms.

[Download the complete ARM64 evidence artifact](https://github.com/kjanat/udm-iptv/actions/runs/35810519259/artifacts/10729548173).
Its `binary-patched-arm64/` directory contains the tested executable, original
source archive, patch/test sources, build metadata and checksum manifest.
Artifacts expire after fourteen days; a verified local copy is retained under
`tests/multicast/artifacts/ci-35810519259/`.
