"""Summarize Ethernet PCAP captures of IPTV IGMP, announcements and carousels."""

import argparse
import collections
import hashlib
import ipaddress
import json
import pathlib
import struct

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument(
    "directory", type=pathlib.Path, help="directory containing PCAP files"
)
root = parser.parse_args().directory
if not root.is_dir():
    parser.error("capture directory does not exist")
reports = {}
keys = {}
ip = lambda data: str(ipaddress.IPv4Address(data))
for path in sorted(root.glob("*.pcap*")):
    data = path.read_bytes()
    if len(data) < 24:
        continue
    if data[:4] not in (b"\xd4\xc3\xb2\xa1", b"\xa1\xb2\xc3\xd4"):
        raise ValueError(f"{path}: expected microsecond classic PCAP")
    endian = "<" if data[:4] == b"\xd4\xc3\xb2\xa1" else ">"
    if struct.unpack_from(endian + "I", data, 20)[0] != 1:
        raise ValueError(f"{path}: expected Ethernet link type")
    position = 24
    udp = collections.Counter()
    igmp = collections.Counter()
    packets = collections.Counter()
    times = []
    while position + 16 <= len(data):
        sec, usec, length, original = struct.unpack_from(
            endian + "IIII", data, position
        )
        position += 16
        frame = data[position : position + length]
        position += length
        if len(frame) < length:
            break
        start = 14
        kind = int.from_bytes(frame[12:14], "big")
        while kind in (0x8100, 0x88A8):
            kind = int.from_bytes(frame[start + 2 : start + 4], "big")
            start += 4
        if kind != 0x0800 or len(frame) < start + 20:
            continue
        header = (frame[start] & 15) * 4
        source = ip(frame[start + 12 : start + 16])
        dest = ip(frame[start + 16 : start + 20])
        protocol = frame[start + 9]
        payload = frame[start + header :]
        times.append(sec + usec / 1e6)
        if protocol == 17 and len(payload) >= 8:
            sport, dport, size, check = struct.unpack_from("!HHHH", payload)
            udp[(source, dest, dport)] += 1
            identity = (
                source,
                dest,
                sport,
                dport,
                size,
                frame[start + 4 : start + 6].hex(),
                hashlib.sha256(payload[8:136]).hexdigest(),
            )
            packets[identity] += 1
        elif protocol == 2 and len(payload) >= 8:
            if payload[0] == 0x22:
                count = int.from_bytes(payload[6:8], "big")
                offset = 8
                for _ in range(count):
                    if len(payload) < offset + 8:
                        break
                    record, aux, sources = struct.unpack_from("!BBH", payload, offset)
                    group = ip(payload[offset + 4 : offset + 8])
                    igmp[(source, "v3-record-" + str(record), group)] += 1
                    offset += 8 + 4 * sources + 4 * aux
            else:
                igmp[(source, hex(payload[0]), ip(payload[4:8]))] += 1
    reports[path.name] = {
        "span_seconds": round(max(times) - min(times), 3) if times else 0,
        "udp": [
            {"source": src, "group": group, "port": port, "packets": n}
            for (src, group, port), n in sorted(udp.items())
        ],
        "igmp": [
            {"source": src, "type": kind, "group": group, "packets": n}
            for (src, kind, group), n in sorted(igmp.items())
        ],
    }
    capture_name = path.name.split(".pcap", 1)[0]
    keys.setdefault(capture_name, collections.Counter()).update(packets)

for name, report in reports.items():
    capture_name = name.split(".pcap", 1)[0]
    prefix = "receiver-" if capture_name.startswith("receiver-") else ""
    upstream_name = prefix + "iptv"
    if capture_name != upstream_name and upstream_name in keys:
        report["matching_udp_headers_and_payload_prefixes"] = sum(
            (keys[capture_name] & keys[upstream_name]).values()
        )

(root / "summary.json").write_text(json.dumps(reports, indent=2) + "\n")
for name, report in reports.items():
    print(
        name,
        "UDP:",
        report["udp"],
        "matching upstream:",
        report.get("matching_udp_headers_and_payload_prefixes", "n/a"),
    )
    for record in report["igmp"]:
        if record["group"].startswith("224.0.250."):
            print(" ", record)
