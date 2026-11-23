"""Decode SAP/SDP announcements from an Ethernet PCAP without extra dependencies."""

import argparse
import collections
import json
import pathlib
import socket
import struct
import zlib

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("capture", type=pathlib.Path, help="Ethernet PCAP file")
args = parser.parse_args()
directory = args.capture.parent
raw = args.capture.read_bytes()
if len(raw) < 24 or raw[:4] not in (b"\xd4\xc3\xb2\xa1", b"\xa1\xb2\xc3\xd4"):
    parser.error("expected microsecond classic PCAP")
endian = "<" if raw[:4] == b"\xd4\xc3\xb2\xa1" else ">"
if struct.unpack_from(endian + "I", raw, 20)[0] != 1:
    parser.error("expected Ethernet link type")
offset = 24
packets = 0
sessions = {}
sources = collections.Counter()
timestamps = []
errors = []
while offset + 16 <= len(raw):
    seconds, fraction, captured, original = struct.unpack_from(
        endian + "IIII", raw, offset
    )
    offset += 16
    frame = raw[offset : offset + captured]
    offset += captured
    if len(frame) != captured:
        break
    try:
        ip_offset = 14
        ether_type = int.from_bytes(frame[12:14], "big")
        while ether_type in (0x8100, 0x88A8):
            ether_type = int.from_bytes(frame[ip_offset + 2 : ip_offset + 4], "big")
            ip_offset += 4
        if ether_type != 0x0800 or frame[ip_offset + 9] != 17:
            continue
        udp_offset = ip_offset + (frame[ip_offset] & 15) * 4
        source_port, destination_port, length, checksum = struct.unpack_from(
            "!HHHH", frame, udp_offset
        )
        destination = socket.inet_ntoa(frame[ip_offset + 16 : ip_offset + 20])
        if destination != "224.0.250.64" or destination_port != 9875:
            continue
        payload = frame[udp_offset + 8 : udp_offset + length]
        packets += 1
        timestamps.append(seconds + fraction / 1e6)
        source = socket.inet_ntoa(frame[ip_offset + 12 : ip_offset + 16])
        sources[source] += 1
        if len(payload) != length - 8:
            raise ValueError("truncated UDP payload; capture with --snaplen 0")
        flags = payload[0]
        if flags >> 5 != 1:
            raise ValueError("unexpected SAP version")
        if flags & 2:
            raise ValueError("encrypted SAP payload")
        sap_offset = 4 + (16 if flags & 0x10 else 4) + payload[1] * 4
        body = payload[sap_offset:]
        if flags & 1:
            body = zlib.decompress(body)
        if not body.startswith(b"v=0"):
            mime, body = body.split(b"\0", 1)
        sdp = body.decode("utf-8", errors="replace").strip()
        fields = collections.defaultdict(list)
        for line in sdp.splitlines():
            if len(line) > 1 and line[1] == "=":
                fields[line[0]].append(line[2:])
        name = fields.get("s", [""])[0]
        connection = fields.get("c", [""])[0]
        media = fields.get("m", [])
        key = (name, connection, tuple(media))
        if key not in sessions:
            sessions[key] = {
                "name": name,
                "connection": connection,
                "media": media,
                "attributes": fields.get("a", []),
                "source": source,
                "deletion": bool(flags & 4),
                "seen": 0,
                "sdp": sdp,
            }
        sessions[key]["seen"] += 1
    except (ValueError, IndexError, struct.error, zlib.error) as error:
        errors.append(str(error))

records = list(sessions.values())
report = {
    "packets": packets,
    "unique_sessions": len(records),
    "observed_span_seconds": round(max(timestamps) - min(timestamps), 3)
    if timestamps
    else 0,
    "sources": dict(sources),
    "decode_errors": errors,
    "sessions": records,
}
(directory / "announcements.json").write_text(json.dumps(report, indent=2) + "\n")
(directory / "announcements.sdp.txt").write_text(
    "\n\n".join(record["sdp"] for record in records) + "\n"
)
print(
    json.dumps(
        {key: value for key, value in report.items() if key != "sessions"}, indent=2
    )
)
for record in records:
    print(
        record["name"],
        "|",
        record["connection"],
        "|",
        "; ".join(record["media"]),
        "| seen",
        record["seen"],
    )
