"""Bounded multicast experiment. All topology lives inside a network-less container."""

import argparse
import hashlib
import json
import os
import platform
import signal
import socket
import struct
import subprocess
import sys
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path

from wire import GROUP, PORT, PROXY_LAN, RECEIVER, SENDER, read_pcap

NAMESPACES = ("sender", "proxy", "receiver", "querier")


def command(*args):
    return subprocess.run(
        args, check=True, text=True, capture_output=True, timeout=10
    ).stdout


def write_json(path, data):
    path.write_text(json.dumps(data, indent=2) + "\n")


def json_lines(path):
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def configure(role, receiver_version):
    # ip netns exec gives this helper a separate mount namespace. Its new procfs
    # exposes writable network sysctls for that network namespace only.
    command("mount", "-t", "proc", "proc", "/proc")
    settings = {}
    for directory in Path("/proc/sys/net/ipv4/conf").iterdir():
        settings[str(directory / "rp_filter")] = "0"
    if role == "proxy":
        settings["/proc/sys/net/ipv4/ip_forward"] = "1"
    if role == "receiver":
        settings["/proc/sys/net/ipv4/conf/lan/force_igmp_version"] = str(
            receiver_version
        )
    for path, value in settings.items():
        Path(path).write_text(value)
    print(json.dumps(settings))


class Lab:
    def __init__(self, output):
        self.output = output
        self.processes = []
        self.namespaces = []
        self.bridges = []

    def start(self, namespace, label, *args):
        log = (self.output / (label + ".log")).open("w")
        try:
            process = subprocess.Popen(
                ["ip", "netns", "exec", namespace, *args],
                stdout=log,
                stderr=subprocess.STDOUT,
                start_new_session=True,
            )
        finally:
            log.close()
        self.processes.append((label, process))
        return process

    def check_alive(self):
        for label, process in self.processes:
            if process.poll() is not None:
                raise RuntimeError(
                    f"{label} exited {process.returncode}; see {label}.log"
                )

    def setup(self, receiver_version, querier_address):
        links = json.loads(command("ip", "-j", "link", "show"))
        if [link["ifname"] for link in links] != ["lo"]:
            raise RuntimeError(
                "refusing topology setup: container must use network_mode: none"
            )
        if command("ip", "netns", "list").strip():
            raise RuntimeError("refusing to reuse existing network namespaces")
        for name in NAMESPACES:
            command("ip", "netns", "add", name)
            self.namespaces.append(name)
            command("ip", "-n", name, "link", "set", "lo", "up")
        for bridge in ("upstream", "downstream"):
            command(
                "ip",
                "link",
                "add",
                bridge,
                "type",
                "bridge",
                "mcast_snooping",
                "0",
                "mcast_querier",
                "0",
            )
            self.bridges.append(bridge)
            command("ip", "link", "set", bridge, "up")
        endpoints = [
            ("sender", "wan", SENDER, "upstream", "send-port"),
            ("proxy", "wan", "198.51.100.20", "upstream", "wan-port"),
            ("proxy", "lan", PROXY_LAN, "downstream", "lan-port"),
            ("receiver", "lan", RECEIVER, "downstream", "recv-port"),
            ("querier", "lan", querier_address, "downstream", "query-port"),
        ]
        for name, interface, address, bridge, port in endpoints:
            command(
                "ip",
                "link",
                "add",
                port,
                "type",
                "veth",
                "peer",
                "name",
                interface,
                "netns",
                name,
            )
            command("ip", "link", "set", port, "master", bridge)
            command("ip", "link", "set", port, "up")
            command(
                "ip", "-n", name, "address", "add", address + "/24", "dev", interface
            )
            command("ip", "-n", name, "link", "set", interface, "up")
        for name in NAMESPACES:
            interface = "wan" if name in ("sender", "proxy") else "lan"
            command("ip", "-n", name, "route", "add", "224.0.0.0/4", "dev", interface)
            settings = command(
                "ip",
                "netns",
                "exec",
                name,
                "python3",
                "/lab/lab.py",
                "configure",
                "--role",
                name,
                "--receiver-version",
                str(receiver_version),
            )
            write_json(self.output / f"{name}-sysctls.json", json.loads(settings))
        bridges = json.loads(
            command("ip", "-d", "-j", "link", "show", "type", "bridge")
        )
        for bridge in bridges:
            details = bridge["linkinfo"]["info_data"]
            if details["mcast_snooping"] != 0 or details["mcast_querier"] != 0:
                raise RuntimeError("bridge would interfere with the proxy experiment")
        write_json(
            self.output / "topology.json", {"endpoints": endpoints, "bridges": bridges}
        )

    def snapshot(self):
        state = {"time": time.time()}
        for name in ("proxy", "receiver"):
            state[name] = {}
            for filename in ("igmp", "ip_mr_vif", "ip_mr_cache"):
                state[name][filename] = command(
                    "ip", "netns", "exec", name, "cat", "/proc/net/" + filename
                )
        with (self.output / "state.jsonl").open("a") as stream:
            stream.write(json.dumps(state) + "\n")

    def stop_processes(self):
        for label, process in reversed(self.processes):
            if process.poll() is None:
                os.killpg(
                    process.pid,
                    signal.SIGINT if label.endswith("capture") else signal.SIGTERM,
                )
        for _label, process in self.processes:
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=3)
        self.processes.clear()

    def close(self):
        self.stop_processes()
        failures = []
        for name in reversed(self.namespaces):
            try:
                command("ip", "netns", "delete", name)
            except subprocess.CalledProcessError as error:
                failures.append(error.stderr)
        for name in reversed(self.bridges):
            try:
                command("ip", "link", "delete", name)
            except subprocess.CalledProcessError as error:
                failures.append(error.stderr)
        write_json(
            self.output / "cleanup.json",
            {
                "errors": failures,
                "namespaces": command("ip", "netns", "list"),
                "links": json.loads(command("ip", "-j", "link", "show")),
            },
        )
        if failures:
            raise RuntimeError("topology cleanup failed; see cleanup.json")


def proxy_command(output, version):
    binary = Path("/opt/proxy/improxy")
    implementation = os.environ["PROXY_IMPLEMENTATION"]
    metadata = {
        "implementation": implementation,
        "revision": Path("/opt/proxy/revision").read_text().strip(),
        "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "kernel": platform.uname()._asdict(),
        "elf": command("readelf", "-h", "-l", "-d", "-n", str(binary)),
    }
    config = output / "proxy.conf"
    config.write_text(
        f"igmp enable version {version}\nmld disable\nquickleave disable\nupstream wan\ndownstream lan\n"
    )
    prefix = []
    if implementation == "firmware":
        if platform.machine() not in ("aarch64", "arm64"):
            raise RuntimeError(
                "firmware mode requires native ARM64, not QEMU user emulation"
            )
        loader = Path("/opt/proxy/lib/ld-linux-aarch64.so.1")
        # Firmware symlinks can be absolute. Resolve them relative to the extracted root.
        while loader.is_symlink():
            target = loader.readlink()
            loader = (
                Path("/opt/proxy") / str(target).lstrip("/")
                if target.is_absolute()
                else loader.parent / target
            )
        loader = loader.resolve(strict=True)
        if not loader.is_relative_to("/opt/proxy"):
            raise RuntimeError("firmware loader escaped the extracted firmware root")
        paths = [
            Path("/opt/proxy/lib/aarch64-linux-gnu"),
            Path("/opt/proxy/usr/lib/aarch64-linux-gnu"),
        ]
        metadata["loader_sha256"] = hashlib.sha256(loader.read_bytes()).hexdigest()
        prefix = [
            str(loader),
            "--inhibit-cache",
            "--library-path",
            ":".join(map(str, paths)),
        ]
        metadata["resolved_libraries"] = command(*prefix, "--list", str(binary))
        metadata["library_hashes"] = {}
        for line in metadata["resolved_libraries"].splitlines():
            if "=> /" in line:
                library = Path(line.split("=> ", 1)[1].split()[0])
                library = library.resolve(strict=True)
                if not library.is_relative_to("/opt/proxy"):
                    raise RuntimeError(
                        f"firmware resolved a non-firmware library: {library}"
                    )
                metadata["library_hashes"][str(library)] = hashlib.sha256(
                    library.read_bytes()
                ).hexdigest()
    write_json(output / "provenance.json", metadata)
    return [
        *prefix,
        str(binary),
        "-c",
        str(config),
        "-d",
        "5",
        "-p",
        "/tmp/improxy.pid",
    ]


def analyze(output, args, start, finish, query_start):
    lan = read_pcap(output / "lan.pcap")
    wan = read_pcap(output / "wan.pcap")
    write_json(output / "igmp.json", {"lan": lan, "wan": wan})
    proxy_queries = [
        event
        for event in lan
        if event["type"] == 0x11
        and event["source"] == PROXY_LAN
        and event["group"] == "0.0.0.0"
        and event["ttl"] == 1
        and event["router_alert"]
    ]
    other_queries = [
        event
        for event in lan
        if event["type"] == 0x11
        and event["source"] in ("192.0.2.10", "192.0.2.30")
        and event["group"] == "0.0.0.0"
        and event["ttl"] == 1
        and event["router_alert"]
    ]
    receiver = [
        row for row in json_lines(output / "receiver.log") if row["kind"] == "received"
    ]
    received = (
        receiver[-1]
        if receiver
        else {"packets": 0, "first_at": None, "last_at": None, "max_gap": None}
    )
    checks = {
        "proxy_general_query_seen": bool(proxy_queries),
        "receiver_membership_report_seen": any(
            event["source"] == RECEIVER and GROUP in event["groups"] for event in lan
        ),
        "upstream_membership_report_seen": any(
            GROUP in event["groups"] for event in wan
        ),
        "multicast_received": received["packets"] >= 50,
        "stream_started": received["first_at"] is not None
        and received["first_at"] - start < 3,
        "stream_still_arriving": received["last_at"] is not None
        and finish - received["last_at"] < 1,
        "no_long_stream_gap": received["max_gap"] is not None
        and received["max_gap"] < 1,
    }
    group_hex = f"{struct.unpack('=I', socket.inet_aton(GROUP))[0]:08X}"
    source_hex = f"{struct.unpack('=I', socket.inet_aton(SENDER))[0]:08X}"
    mfc_packets = []
    for state in json_lines(output / "state.jsonl"):
        for line in state["proxy"]["ip_mr_cache"].splitlines()[1:]:
            fields = line.split()
            if len(fields) >= 7 and fields[:2] == [group_hex, source_hex]:
                mfc_packets.append(int(fields[3]))
    checks["multicast_forwarding_cache_increased"] = (
        len(mfc_packets) >= 2 and mfc_packets[-1] > mfc_packets[0]
    )
    if query_start is not None:
        checks["second_query_reached_proxy_interface"] = bool(other_queries)
    for interface in ("lan", "wan"):
        capture_log = (output / (interface + "-capture.log")).read_text()
        checks[interface + "_capture_no_kernel_drops"] = (
            "\n0 packets dropped by kernel" in capture_log
        )
    election = "not_evaluated"
    if args.mode == "scenario":
        if args.scenario == "lower" and other_queries:
            late = [
                event
                for event in proxy_queries
                if event["time"] > other_queries[0]["time"] + 1
            ]
            election = "fail" if late else "pass"
        elif args.scenario == "higher" and other_queries:
            late = [
                event
                for event in proxy_queries
                if event["time"] > other_queries[0]["time"] + 1
            ]
            election = "pass" if late else "fail"
        elif args.scenario == "baseline":
            election = "not_applicable"
        else:
            election = "inconclusive"
    return {
        "checks": checks,
        "election": election,
        "receiver": received,
        "mfc_packet_counts": mfc_packets,
        "proxy_queries": proxy_queries,
        "other_queries": other_queries,
        "started_at": start,
        "finished_at": finish,
        "duration": finish - start,
        "normal_query_interval_covered": args.mode == "scenario"
        and args.duration >= 170,
        "membership_timer_covered": args.mode == "scenario" and args.duration >= 300,
    }


def experiment(args):
    if args.mode == "scenario" and not 45 <= args.duration <= 900:
        raise ValueError("scenario duration must be 45..900 seconds")
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    output = Path("/artifacts") / (stamp + "-" + uuid.uuid4().hex[:8])
    output.mkdir(parents=True)
    write_json(output / "parameters.json", vars(args))
    lab = Lab(output)
    result = {"status": "error"}
    try:
        proxy = proxy_command(output, args.proxy_version)
        address = (
            "192.0.2.30"
            if args.mode == "smoke" or args.scenario == "higher"
            else "192.0.2.10"
        )
        lab.setup(args.receiver_version, address)
        for interface in ("wan", "lan"):
            lab.start(
                "proxy",
                interface + "-capture",
                "tcpdump",
                "-Z",
                "root",
                "-i",
                interface,
                "-p",
                "-U",
                "-s",
                "0",
                "-w",
                str(output / (interface + ".pcap")),
                f"igmp or (udp and dst host {GROUP} and dst port {PORT})",
            )
        # Wait for both capture sockets before starting any membership or proxy traffic.
        deadline = time.monotonic() + 3
        while not all(
            "listening on" in (output / (name + "-capture.log")).read_text()
            for name in ("wan", "lan")
        ):
            lab.check_alive()
            if time.monotonic() > deadline:
                raise RuntimeError("capture startup timeout")
            time.sleep(0.05)
        lab.start("proxy", "proxy", *proxy)
        deadline = time.monotonic() + 3
        while True:
            lab.check_alive()
            vifs = command("ip", "netns", "exec", "proxy", "cat", "/proc/net/ip_mr_vif")
            if "wan" in vifs and "lan" in vifs:
                break
            if time.monotonic() > deadline:
                raise RuntimeError("proxy did not register both multicast VIFs")
            time.sleep(0.05)
        lab.start("receiver", "receiver", "python3", "/lab/traffic.py", "receiver")
        deadline = time.monotonic() + 3
        while '"kind": "ready"' not in (output / "receiver.log").read_text():
            lab.check_alive()
            if time.monotonic() > deadline:
                raise RuntimeError("receiver join timeout")
            time.sleep(0.05)
        start = time.time()
        lab.start("sender", "sender", "python3", "/lab/traffic.py", "sender")
        start_mono = time.monotonic()
        duration = 5 if args.mode == "smoke" else args.duration
        query_delay = 3 if args.mode == "smoke" else 10
        query_start, next_snapshot = None, 0.0
        while time.monotonic() - start_mono < duration:
            elapsed = time.monotonic() - start_mono
            lab.check_alive()
            if (
                elapsed >= query_delay
                and query_start is None
                and (args.mode == "smoke" or args.scenario != "baseline")
            ):
                lab.start(
                    "querier",
                    "querier",
                    "python3",
                    "/lab/traffic.py",
                    "querier",
                    "--address",
                    address,
                    "--version",
                    str(args.query_version),
                )
                query_start = time.time()
            if elapsed >= next_snapshot:
                lab.snapshot()
                next_snapshot = elapsed + (1 if args.mode == "smoke" else 30)
            time.sleep(0.05)
        lab.snapshot()
        finish = time.time()
        lab.stop_processes()
        result = analyze(output, args, start, finish, query_start)
        result["status"] = (
            "pass"
            if all(result["checks"].values())
            and result["election"] not in ("fail", "inconclusive")
            else "fail"
        )
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        result = {"status": "error", "error": str(error)}
        if isinstance(error, subprocess.CalledProcessError):
            result["stderr"] = error.stderr
    finally:
        try:
            lab.close()
        except (OSError, RuntimeError, subprocess.SubprocessError) as error:
            result["status"] = "error"
            result["cleanup_error"] = str(error)
        write_json(output / "result.json", result)
        paths = sorted(path for path in output.iterdir() if path.is_file())
        (output / "SHA256SUMS").write_text(
            "".join(
                f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n"
                for path in paths
            )
        )
    print(json.dumps(dict(output=str(output), **result), indent=2), flush=True)
    return {"pass": 0, "fail": 1, "error": 2}[result["status"]]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["smoke", "scenario", "configure"])
    parser.add_argument(
        "--scenario", choices=["baseline", "lower", "higher"], default="baseline"
    )
    parser.add_argument("--duration", type=int, default=360)
    parser.add_argument("--proxy-version", type=int, choices=[2, 3], default=3)
    parser.add_argument("--receiver-version", type=int, choices=[2, 3], default=2)
    parser.add_argument("--query-version", type=int, choices=[2, 3], default=3)
    parser.add_argument("--role", choices=NAMESPACES)
    args = parser.parse_args()
    if args.mode == "configure":
        configure(args.role, args.receiver_version)
        return 0
    signal.signal(signal.SIGTERM, lambda _sig, _frame: sys.exit(130))
    return experiment(args)


if __name__ == "__main__":
    sys.exit(main())
