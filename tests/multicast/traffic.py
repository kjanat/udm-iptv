"""Synthetic UDP endpoints and a query source that participates in election."""

import argparse
import ipaddress
import json
import signal
import socket
import struct
import time

from wire import GROUP, PORT, RECEIVER, SENDER, igmp, query

running = True


def stop(_signal, _frame):
    global running
    running = False


def emit(kind, **fields):
    print(json.dumps(dict(time=time.time(), kind=kind, **fields)), flush=True)


def sender():
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        sock.setsockopt(
            socket.IPPROTO_IP, socket.IP_MULTICAST_IF, socket.inet_aton(SENDER)
        )
        sock.setsockopt(socket.IPPROTO_IP, socket.IP_MULTICAST_TTL, 16)
        sequence = 0
        emit("ready")
        while running:
            sock.sendto(struct.pack("!Qd", sequence, time.time()), (GROUP, PORT))
            sequence += 1
            time.sleep(0.02)
        emit("sent", packets=sequence)


def receiver():
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        # Only the fixture group counts as multicast delivery, not same-port unicast.
        sock.bind((GROUP, PORT))
        sock.setsockopt(
            socket.IPPROTO_IP,
            socket.IP_ADD_MEMBERSHIP,
            socket.inet_aton(GROUP) + socket.inet_aton(RECEIVER),
        )
        sock.settimeout(0.2)
        emit("ready", group=GROUP)
        count, missing, reordered = 0, 0, 0
        previous = None
        first_at, last_at, max_gap = None, None, 0.0
        next_status = time.monotonic() + 1
        while running:
            try:
                data, address = sock.recvfrom(2048)
                if address[0] != SENDER or len(data) != 16:
                    continue
                sequence, _sent_at = struct.unpack("!Qd", data)
                now = time.time()
                if previous is not None:
                    missing += max(0, sequence - previous - 1)
                    reordered += int(sequence <= previous)
                if last_at is not None:
                    max_gap = max(max_gap, now - last_at)
                first_at = now if first_at is None else first_at
                last_at, previous = now, sequence
                count += 1
            except TimeoutError:
                pass
            if time.monotonic() >= next_status or not running:
                emit(
                    "received",
                    packets=count,
                    missing=missing,
                    reordered=reordered,
                    first_at=first_at,
                    last_at=last_at,
                    max_gap=max_gap,
                )
                next_status = time.monotonic() + 1
        emit(
            "received",
            packets=count,
            missing=missing,
            reordered=reordered,
            first_at=first_at,
            last_at=last_at,
            max_gap=max_gap,
        )


def querier(address, version, query_limit):
    with socket.socket(socket.AF_INET, socket.SOCK_RAW, socket.IPPROTO_IGMP) as sock:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_BINDTODEVICE, b"lan\0")
        sock.setsockopt(
            socket.IPPROTO_IP, socket.IP_MULTICAST_IF, socket.inet_aton(address)
        )
        sock.setsockopt(socket.IPPROTO_IP, socket.IP_MULTICAST_TTL, 1)
        sock.setsockopt(socket.IPPROTO_IP, socket.IP_OPTIONS, b"\x94\x04\x00\x00")
        sock.settimeout(0.1)
        next_query, other_until, startup = time.monotonic(), 0.0, 2
        sent = 0
        emit("ready", address=address, version=version)
        while running:
            now = time.monotonic()
            if (
                now >= next_query
                and now >= other_until
                and (query_limit == 0 or sent < query_limit)
            ):
                sock.sendto(query(version), ("224.0.0.1", 0))
                sent += 1
                emit("query", address=address, version=version)
                startup = max(0, startup - 1)
                next_query = now + (31.25 if startup else 125)
            try:
                packet, _ = sock.recvfrom(65535)
            except TimeoutError:
                continue
            event = igmp(packet)
            if (
                event
                and event["type"] == 0x11
                and event["group"] == "0.0.0.0"
                and event["ttl"] == 1
                and event["router_alert"]
                and ipaddress.IPv4Address("0.0.0.0")
                < ipaddress.IPv4Address(event["source"])
                < ipaddress.IPv4Address(address)
            ):
                # This fixture only generates default QRV=2, QI=125, QRI=10 queries.
                other_until = time.monotonic() + 255
                emit("lower_querier", address=event["source"])


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("role", choices=["sender", "receiver", "querier"])
    parser.add_argument("--address", default="192.0.2.10")
    parser.add_argument("--version", type=int, choices=[2, 3], default=3)
    parser.add_argument("--query-limit", type=int, default=0)
    args = parser.parse_args()
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    if args.role == "sender":
        sender()
    elif args.role == "receiver":
        receiver()
    else:
        querier(args.address, args.version, args.query_limit)
