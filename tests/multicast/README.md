# Isolated multicast proxy fixture

Checks whether multicast actually traverses IMProxy before running querier-election
experiments. The default smoke test sends synthetic UDP for **five seconds**; no
television, KPN connection, router credentials or live network is involved.

```text
sender                  proxy                         receiver
198.51.100.10 -- WAN -- 198.51.100.20 | 192.0.2.20 -- LAN -- 192.0.2.40
                                                   |
                                    second querier: 192.0.2.10 or .30
```

Compose runs one lab container with `network_mode: none`. Four Linux network
namespaces separate the participants. Two internal bridges have no IP addresses,
no IGMP snooping and no bridge querier. WAN and LAN meet only through the proxy;
the sender cannot deliver directly to the receiver. Nothing attaches to the host
LAN, publishes a port or mounts the Docker socket.

## Build, then prove the fixture works

From the repository root, with Docker Engine and Compose available:

```sh
docker compose -f tests/multicast/compose.yaml build
python3 tests/multicast/run.py smoke
```

On Windows, use `python` instead of `python3`. Linux containers are required.
The wrapper verifies the image and Docker daemon architectures match, records
image/kernel provenance, and removes its container even after failure.

The build runs fast offline packet tests. The smoke test requires:

- Both multicast VIFs successfully registered by the proxy.
- A valid proxy general query, receiver report and upstream membership report.
- At least 50 received sequence-numbered UDP packets, prompt initial delivery,
  continuing delivery at the end and no observed gap exceeding one second.
- A valid query from the higher-address second querier captured on the proxy's
  LAN interface. This only proves the stimulus reaches the proxy.
- No kernel packet drops reported by either capture process.

The five seconds **do not test querier-election convergence, the normal query
interval, or membership expiry**. A successful smoke test is a prerequisite for
those experiments, not evidence that they pass.

## Two implementations, explicitly separated

| Mode                 | Program                                                                            | Required Docker daemon |
| -------------------- | ---------------------------------------------------------------------------------- | ---------------------- |
| `upstream` (default) | Native build of `haibbo/improxy` commit `5a9d153d10e19ef835af9fc5f2a971799677a7ed` | Native AMD64 or ARM64  |
| `firmware`           | Unmodified executable, loader and libraries from a pinned UniFi OCI image          | Native ARM64           |

Upstream source is fetched at the fixed commit without timer or behavior patches.
Source results do not establish shipped-binary behavior.

Firmware mode defaults to UDM Pro 5.1.33, pinned by OCI manifest digest in Compose.
Its executable SHA-256 is
`75d7a7a5dc5bc23709c3b11d331ece2d78678a3fc59692a2d50ae870eaa5e822`.
Other image tags/digests can be selected explicitly; runtime records the selected
revision, actual executable hash, ELF metadata, loader and resolved library hashes.

On a native ARM64 Docker host:

```sh
export PROXY_IMPLEMENTATION=firmware
export FIRMWARE_IMAGE=ghcr.io/kjanat/unifi-os:udmpro-5.1.33@sha256:5d6138dd461c4d265380eb550f5e609b072268e50205007cdd050d09f1d38bf8
docker compose -f tests/multicast/compose.yaml build
python3 tests/multicast/run.py smoke --implementation firmware
```

PowerShell uses `$env:PROXY_IMPLEMENTATION = 'firmware'` and
`$env:FIRMWARE_IMAGE = '…'` for these variables. Firmware images can be assembled
on AMD64, but this fixture deliberately rejects running them there: QEMU user-mode
socket translation lacks the multicast-routing operations needed by IMProxy.
Setting Compose `platform: linux/arm64` would not solve that limitation.

[The ARM64 workflow](../../.github/workflows/multicast.yml) builds and smoke-tests
the source implementation and pinned UDM Pro/UCG Max 5.1.33 firmware images on
`ubuntu-24.04-arm`. It checks both host and daemon architecture and uploads evidence
even on failure. It never automatically runs the longer scenarios.

## Explicit longer experiments

Run these only after the smoke test passes:

```sh
python3 tests/multicast/run.py scenario --scenario baseline --duration 360
python3 tests/multicast/run.py scenario --scenario lower --duration 360
python3 tests/multicast/run.py scenario --scenario higher --duration 360
```

Add `--implementation firmware` to use the firmware image already built.
Each invocation creates fresh namespaces, processes and membership state.

The second querier starts after ten seconds. `.10` should win against proxy `.20`;
`.30` should yield. The query source implements that election response with the
default 255-second other-querier interval. It uses TTL 1, Router Alert, valid IGMP
checksums, QRV 2, query interval 125 seconds and max response time 10 seconds.
It is a small controlled stimulus, not a complete replacement for a switch.

`--proxy-version`, `--receiver-version` and `--query-version` independently accept
`2` or `3`. Defaults are proxy v3, receiver v2 and second-querier v3. The Linux
receiver performs real kernel membership/report processing. Captures record the
versions actually observed; the upstream host behavior belongs to the host kernel.

Minimum scenario duration is 45 seconds. With this source revision, proxy queries
are scheduled near 2, 33, 158 and 283 seconds after proxy startup. A 45-second run
only covers startup queries; 360 seconds includes normal query intervals and
extends past the default 260-second membership timer. No timers are accelerated.

`result.json` separates election results from forwarding checks. A lower-address
query followed by continued proxy queries fails the election assertion even when
UDP reception remains healthy. This is not, by itself, a reproduction of a frozen
television picture. Forwarding changes under a correctly elected replacement also
need interpretation under RFC 4605's forwarding rules and single-proxy exception.

## Evidence and limitations

Each run writes a new directory under ignored `tests/multicast/artifacts/`:

- `wan.pcap`, `lan.pcap`: full IGMP and the small synthetic UDP payloads.
- Proxy, receiver, sender, querier and capture logs with timestamps/counters.
- `state.jsonl`: IGMP memberships, multicast VIFs and forwarding-cache snapshots.
- `proxy.conf`, `parameters.json`, `topology.json` and namespace sysctl settings.
- `provenance.json`, decoded `igmp.json`, `result.json`, `cleanup.json`, `SHA256SUMS`.
- Alongside the run directory: host wrapper image ID and Docker/kernel metadata.

Exit codes: `0` checks pass, `1` observed assertion failure, `2` fixture/environment
error. Missing multicast support or failed proxy startup is an error, never a pass.

`NET_ADMIN` and `NET_RAW` configure the private topology and captures. `SYS_ADMIN`
and `apparmor:unconfined` permit network namespace mounts and a child mount of
procfs for per-namespace sysctls. `SETUID`/`SETGID` allow tcpdump's explicit `-Z root`
initialization; `DAC_OVERRIDE` permits writing the sole host bind mount, the
artifacts directory, when its owner is the CI runner user. No host namespaces, devices or network mounts are
used. Build-time package/source/image downloads need internet; test traffic does not.

Docker shares its Linux host kernel. Neither mode boots a UniFi kernel, models a
switch ASIC, duplicates receiver firmware, or establishes the reporter's cause of
failure. All capture clocks come from the same host.

Protocol references: [RFC 4605 §3](https://www.rfc-editor.org/rfc/rfc4605.html#section-3),
[RFC 9776 §6.6.2](https://www.rfc-editor.org/rfc/rfc9776.html#section-6.6.2).
Architecture references: [Docker network mode](https://docs.docker.com/reference/compose-file/services/#network_mode),
[QEMU user-mode syscall implementation](https://github.com/qemu/qemu/blob/master/linux-user/syscall.c).
