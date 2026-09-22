"""Host-side launcher: guard native architecture, retain provenance, remove its container."""

import argparse
import json
import os
import subprocess
import sys
import uuid
from pathlib import Path


def normalized_architecture(value):
    return {
        "aarch64": "arm64",
        "arm64": "arm64",
        "x86_64": "amd64",
        "amd64": "amd64",
    }.get(value, value)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["smoke", "scenario"])
    parser.add_argument(
        "--implementation", choices=["upstream", "firmware"], default="upstream"
    )
    parser.add_argument("--firmware-image")
    args, options = parser.parse_known_args()
    directory = Path(__file__).resolve().parent
    environment = dict(os.environ, PROXY_IMPLEMENTATION=args.implementation)
    if args.firmware_image:
        environment["FIRMWARE_IMAGE"] = args.firmware_image
    compose = ["docker", "compose", "-f", str(directory / "compose.yaml")]
    info = json.loads(
        subprocess.check_output(["docker", "info", "--format", "{{json .}}"], text=True)
    )
    architecture = normalized_architecture(info["Architecture"])
    if args.implementation == "firmware" and architecture != "arm64":
        parser.error(
            "firmware requires a native ARM64 Docker daemon; QEMU user mode cannot implement its multicast sockets"
        )
    image_name = "udm-iptv-multicast:" + args.implementation
    image = json.loads(
        subprocess.check_output(["docker", "image", "inspect", image_name], text=True)
    )[0]
    if normalized_architecture(image["Architecture"]) != architecture:
        parser.error(
            "fixture image must match the Docker daemon architecture; rebuild without --platform"
        )
    if args.implementation == "firmware":
        config = json.loads(
            subprocess.check_output(
                [*compose, "config", "--format", "json"],
                env=environment,
                text=True,
            )
        )
        requested = config["services"]["lab"]["build"]["args"]["FIRMWARE_IMAGE"]
        built_from = (
            image["Config"].get("Labels", {}).get("org.opencontainers.image.base.name")
        )
        if requested != built_from:
            parser.error("built firmware differs from FIRMWARE_IMAGE; rebuild first")
    artifacts = directory / "artifacts"
    artifacts.mkdir(exist_ok=True)
    name = "iptv-multicast-" + uuid.uuid4().hex[:12]
    provenance = {
        "image_id": image["Id"],
        "image_architecture": image["Architecture"],
        "daemon_architecture": architecture,
        "daemon_kernel": info["KernelVersion"],
        "engine_version": info["ServerVersion"],
        "implementation": args.implementation,
        "command": [args.mode, *options],
    }
    (artifacts / (name + ".json")).write_text(json.dumps(provenance, indent=2) + "\n")
    try:
        completed = subprocess.run(
            [
                *compose,
                "run",
                "--rm",
                "--no-deps",
                "--name",
                name,
                "lab",
                args.mode,
                *options,
            ],
            env=environment,
            check=False,
        )
        return completed.returncode
    finally:
        # Only this invocation's named container; no project-wide or image cleanup.
        subprocess.run(
            ["docker", "rm", "-f", name],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
            timeout=15,
        )


if __name__ == "__main__":
    sys.exit(main())
