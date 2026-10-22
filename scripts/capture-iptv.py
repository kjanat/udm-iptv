"""Capture IPTV IGMP, SAP announcements and carousels on several router interfaces."""

import argparse
import datetime
import json
import pathlib
import re
import shlex
import subprocess
import sys


REMOTE = r"""
set -eu
duration=$1
snaplen=$2
packet_limit=$3
filter=$4
shift 4
capture_dir=$(mktemp -d /tmp/iptv-carousel-probe.XXXXXXXX)
printf '%s\n' "$capture_dir"
for iface do
    (
        set +e
        timeout "$duration" tcpdump -Z root -p -ni "$iface" -s "$snaplen" -U \
            -c "$packet_limit" -w "$capture_dir/$iface.pcap" \
            "$filter" \
            >"$capture_dir/$iface.log" 2>&1
        status=$?
        printf '%s\n' "$status" >"$capture_dir/$iface.status"
    ) &
done
wait
for iface do
    printf '\n%s\n' "$iface"
    cat "$capture_dir/$iface.log"
done
"""


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--host", required=True, help="SSH destination, e.g. root@router"
    )
    parser.add_argument("--interfaces", nargs="+", required=True)
    parser.add_argument(
        "--duration", type=int, default=150, help="seconds (default: 150)"
    )
    parser.add_argument(
        "--snaplen", type=int, default=256, help="use 0 for complete packets"
    )
    parser.add_argument("--packet-limit", type=int, default=100000)
    parser.add_argument(
        "--filter",
        default="igmp or (udp and dst net 224.0.250.0/24 and (dst port 9875 or dst port 64000))",
        help="tcpdump filter expression",
    )
    parser.add_argument("--output", type=pathlib.Path)
    args = parser.parse_args()
    if args.host.startswith("-") or any(c.isspace() for c in args.host):
        parser.error("host must be a single SSH destination")
    if not 1 <= args.duration <= 3600:
        parser.error("duration must be between 1 and 3600 seconds")
    if args.snaplen != 0 and not 128 <= args.snaplen <= 262144:
        parser.error("snaplen must be 0 or between 128 and 262144")
    if args.packet_limit < 1:
        parser.error("packet-limit must be positive")
    if len(set(args.interfaces)) != len(args.interfaces):
        parser.error("interfaces must be unique")
    if any(
        not re.fullmatch(r"[A-Za-z0-9_.:-]{1,15}", name) for name in args.interfaces
    ):
        parser.error("invalid interface name")
    now = datetime.datetime.now(datetime.timezone.utc)
    output = args.output or (
        pathlib.Path(__file__).resolve().parents[1]
        / "captures"
        / now.strftime("%Y%m%dT%H%M%S.%fZ")
    )
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    metadata = {
        "started_at": now.isoformat(),
        "host": args.host,
        "interfaces": args.interfaces,
        "duration_seconds": args.duration,
        "snaplen": args.snaplen,
        "packet_limit": args.packet_limit,
        "filter": args.filter,
    }
    command = "sh -s -- " + shlex.join(
        [
            str(args.duration),
            str(args.snaplen),
            str(args.packet_limit),
            args.filter,
            *args.interfaces,
        ]
    )
    print(
        f"Capturing {', '.join(args.interfaces)} on {args.host}; output: {output}",
        flush=True,
    )
    result = subprocess.run(
        ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", args.host, command],
        input=REMOTE,
        text=True,
        stdout=subprocess.PIPE,
        check=False,
    )
    if result.returncode:
        print(result.stdout, file=sys.stderr)
        return result.returncode
    remote_dir, _, log = result.stdout.partition("\n")
    if not re.fullmatch(r"/tmp/iptv-carousel-probe\.[A-Za-z0-9]+", remote_dir):
        raise RuntimeError("unexpected remote capture directory: " + remote_dir)
    metadata["remote_directory"] = remote_dir
    metadata["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    (output / "capture.json").write_text(json.dumps(metadata, indent=2) + "\n")
    print(log)
    subprocess.run(
        ["scp", "-r", f"{args.host}:{remote_dir}/.", str(output)], check=True
    )
    failed = []
    for iface in args.interfaces:
        status = (output / f"{iface}.status").read_text().strip()
        if status not in ("0", "124"):
            failed.append(f"{iface}: exit {status}")
    if failed:
        print("Capture errors: " + "; ".join(failed), file=sys.stderr)
        return 1
    print(f"Saved captures in {output}; remote copies remain in {remote_dir}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
