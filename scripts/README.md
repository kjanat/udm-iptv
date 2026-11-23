# IPTV packet probes

Run these tools from a workstation with Python 3 and SSH access to the router.
The router needs `tcpdump`, `timeout`, and root access. No Python packages are required.

## Capture both sides of the proxy

Choose the IPTV upstream and the receiver's actual LAN bridge. For example:

```sh
python3 scripts/capture-iptv.py --host root@router --interfaces iptv br0 br2 switch0.1 switch0.2
```

Capture starts on the selected interfaces together and lasts 150 seconds by default.
Boot the receiver or trigger its software check during that window. The probe records
IGMP, SAP traffic to `224.0.250.0/24:9875`, and package traffic to
`224.0.250.0/24:64000`. It does not join groups or change proxy/network configuration.

Files go to a new timestamped directory under `captures/`. That directory's
`.gitignore` excludes captures, private addresses, metadata and generated analysis.
Remote copies remain in the router's temporary directory printed by the script.

Each capture stops at its duration or packet limit, whichever comes first.
The default limit is 100,000 packets per interface, with a 256-byte snapshot.
Use `--duration`, `--packet-limit` and `--snaplen` to change these limits.
Use `--filter 'udp and dst host MULTICAST_ADDRESS'` to probe a TV stream instead.
With `--snaplen 0`, complete packets can consume substantial space on the router.
The workstation command waits until capture completes, then retrieves files.
If interrupted, the router's capture processes still stop at their limits.
Logs include packet counts and kernel drop counts; `.status` files contain process
exit codes (`124` is the normal duration timeout).
Reconfiguring the service may recreate the IPTV VLAN and end an upstream capture
with "The interface disappeared". Start a fresh capture after reconfiguration
before comparing upstream and downstream delivery.

## Summarize delivery and receiver requests

```sh
python3 scripts/analyze-iptv-captures.py captures/CAPTURE_DIRECTORY
```

This writes `summary.json` and prints UDP source/group/port counts and IGMP reports
for announcement/package groups. It accepts Ethernet classic PCAP with microsecond
timestamps, including truncated packet snapshots and 802.1Q headers.

For captures named `iptv.pcap*`, upstream UDP headers and the first 128 bytes of payload
are matched against downstream samples. Historical captures with a `receiver-` prefix
are matched to their corresponding `receiver-iptv.pcap*` capture. These matches show
the same packet samples at two router interfaces. A bridge capture does not establish
receipt or installation by the receiver itself. Check the capture time windows and
drop counters before treating missing packets as a forwarding failure.

## Decode full SAP announcements

Capture a complete announcement payload with a short full-packet probe:

```sh
python3 scripts/capture-iptv.py --host root@router --interfaces iptv --duration 35 --snaplen 0
python3 scripts/decode-sap.py captures/CAPTURE_DIRECTORY/iptv.pcap
```

The decoder selects `224.0.250.64:9875`, reads SAP headers, and extracts SDP session
names, connection addresses, media descriptions, versions, digests and targets.
It writes `announcements.json` and `announcements.sdp.txt` beside the capture.
Compressed SAP payloads are supported; encrypted payloads are reported as errors.
Use full packet captures here: the 256-byte default can truncate SDP descriptions.
