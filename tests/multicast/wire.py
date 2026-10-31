"""Small, strict decoder for the Ethernet/IPv4/IGMP captures this fixture writes."""

import socket
import struct
from pathlib import Path

GROUP = "239.192.0.1"
PORT = 5000
PROXY_LAN = "192.0.2.20"
RECEIVER = "192.0.2.40"
SENDER = "198.51.100.10"


def checksum(data: bytes) -> int:
    padded = data + b"\0" * (len(data) % 2)
    total = sum(struct.unpack(f"!{len(padded) // 2}H", padded))
    while total >> 16:
        total = (total & 65535) + (total >> 16)
    return (~total) & 65535


def query(version: int) -> bytes:
    if version not in (2, 3):
        raise ValueError("query version must be 2 or 3")
    body = struct.pack("!BBH4s", 0x11, 100, 0, bytes(4))
    if version == 3:
        body += struct.pack("!BBH", 2, 125, 0)
    return body[:2] + struct.pack("!H", checksum(body)) + body[4:]


def router_alert(options: bytes) -> bool:
    offset = 0
    while offset < len(options):
        kind = options[offset]
        if kind == 0:
            break
        if kind == 1:
            offset += 1
            continue
        if offset + 2 > len(options):
            raise ValueError("truncated IPv4 option")
        size = options[offset + 1]
        if size < 2 or offset + size > len(options):
            raise ValueError("invalid IPv4 option length")
        if options[offset : offset + size] == b"\x94\x04\x00\x00":
            return True
        offset += size
    return False


def igmp(packet: bytes) -> dict | None:
    if len(packet) < 20 or packet[0] >> 4 != 4:
        raise ValueError("truncated or non-IPv4 packet")
    if packet[9] != socket.IPPROTO_IGMP:
        return None
    header_size = (packet[0] & 15) * 4
    total_size = struct.unpack_from("!H", packet, 2)[0]
    if header_size < 20 or total_size < header_size + 8 or len(packet) < total_size:
        raise ValueError("truncated IGMP packet")
    if struct.unpack_from("!H", packet, 6)[0] & 0x3FFF:
        raise ValueError("fragmented IGMP packet")
    body = packet[header_size:total_size]
    if checksum(packet[:header_size]) or checksum(body):
        raise ValueError("invalid IP/IGMP checksum")
    result = {
        "source": socket.inet_ntoa(packet[12:16]),
        "destination": socket.inet_ntoa(packet[16:20]),
        "ttl": packet[8],
        "router_alert": router_alert(packet[20:header_size]),
        "type": body[0],
        "groups": [],
    }
    if body[0] == 0x11:
        if len(body) == 8:
            result["version"] = 2 if body[1] else 1
        elif (
            len(body) >= 12
            and len(body) == 12 + 4 * struct.unpack_from("!H", body, 10)[0]
        ):
            result.update(version=3, qrv=body[8] & 7, qqic=body[9])
        else:
            raise ValueError("invalid query length")
        result["group"] = socket.inet_ntoa(body[4:8])
        result["max_response_code"] = body[1]
    elif body[0] in (0x12, 0x16, 0x17):
        if len(body) != 8:
            raise ValueError("invalid v1/v2 report length")
        result["groups"] = [socket.inet_ntoa(body[4:8])]
    elif body[0] == 0x22:
        offset = 8
        for _ in range(struct.unpack_from("!H", body, 6)[0]):
            if offset + 8 > len(body):
                raise ValueError("truncated v3 record")
            sources = struct.unpack_from("!H", body, offset + 2)[0]
            result["groups"].append(socket.inet_ntoa(body[offset + 4 : offset + 8]))
            offset += 8 + 4 * sources + 4 * body[offset + 1]
        if offset != len(body):
            raise ValueError("invalid v3 report length")
    return result


def read_pcap(path: Path) -> list[dict]:
    events = []
    with path.open("rb") as stream:
        header = stream.read(24)
        formats = {b"\xd4\xc3\xb2\xa1": "<", b"\xa1\xb2\xc3\xd4": ">"}
        if len(header) != 24 or header[:4] not in formats:
            raise ValueError("expected microsecond PCAP")
        endian = formats[header[:4]]
        if struct.unpack_from(endian + "I", header, 20)[0] != 1:
            raise ValueError("expected Ethernet capture")
        while record := stream.read(16):
            if len(record) != 16:
                raise ValueError("truncated PCAP record")
            seconds, micros, captured, original = struct.unpack(endian + "IIII", record)
            frame = stream.read(captured)
            if len(frame) != captured or captured != original or captured < 14:
                raise ValueError("truncated Ethernet frame")
            protocol = struct.unpack_from("!H", frame, 12)[0]
            offset = 14
            while protocol in (0x8100, 0x88A8):
                if offset + 4 > len(frame):
                    raise ValueError("truncated VLAN header")
                protocol = struct.unpack_from("!H", frame, offset + 2)[0]
                offset += 4
            if protocol == 0x0800:
                event = igmp(frame[offset:])
                if event is not None:
                    events.append(dict(time=seconds + micros / 1e6, **event))
    return events
