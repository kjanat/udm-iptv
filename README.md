# udm-iptv

Routed IPTV for UniFi OS, persistent across firmware updates.

## Install

Install the latest release on the console:

```sh
curl -fLO https://github.com/kjanat/udm-iptv/releases/latest/download/udm-iptv-arm64.deb
sudo apt install ./udm-iptv-arm64.deb
```

Installation verifies stable proxy readiness before reporting success.

### Standalone binary

```sh
curl -fLO https://github.com/kjanat/udm-iptv/releases/latest/download/udm-iptv-linux-arm64
curl -fLO https://github.com/kjanat/udm-iptv/releases/latest/download/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
chmod +x udm-iptv-linux-arm64
sudo ./udm-iptv-linux-arm64 install
```

`install --dry-run` previews without root, changes, telemetry or health checks.

`--non-interactive` skips prompts. `apt install` performs real installation.

Configuration: `/data/udm-iptv/config.json`. Legacy settings migrate automatically.

Missing system proxies use preserved binaries and libraries offline.

### Upgrade from v4

On v4.3.2, upgrade to the Go preview with:

```sh
udm-iptv upgrade --prerelease
```

Older v4 versions should first run `udm-iptv upgrade` to update their installer.
The package imports existing settings and transfers the matching v4 IPTV VLAN.
See [v4 upgrade details](docs/troubleshooting.md#upgrade-from-v4).

## Commands

```sh
udm-iptv configure
udm-iptv status
udm-iptv diagnose
udm-iptv start
udm-iptv stop
udm-iptv restart
udm-iptv upgrade
udm-iptv uninstall
```

`stop --for 30m` starts IPTV again after thirty minutes.

`status` shows when a pause ends.

`start` resumes early; running services remain uninterrupted.

`restart`, `stop` and uninstall cancel scheduled starts.

Uninstall retains `/data/udm-iptv/.lock`, protecting later installations from concurrent changes.

Unrelated files survive package purge.

`uninstall` or `uninstall --purge` removes persisted installation data.

`--keep-data` preserves generated data, including diagnostic captures.

`--keep-config` retains configuration while removing diagnostics.

Retention options cannot be combined.

dpkg still removes its packaged executable.

Pauses end at reboot; automatic startup still applies.

Failed scheduling attempts recovery and reports the outcome.

`upgrade --dry-run` shows what would be installed.

Preview builds automatically follow prereleases.

Stable builds follow stable releases unless `--prerelease` is supplied.

Downgrades require `--force`.

See `udm-iptv <command> --help` for options.

## Diagnostics

Record diagnostics for 30 minutes:

```sh
udm-iptv diagnose --capture 30m --format both --follow
```

Captures default to text and JSON Lines.

One-shot diagnosis defaults to text.

`diag` aliases `diagnose`; `--format json` aliases `--format jsonl`.

`--format text` or `--format jsonl` selects one capture format.

`q` or Ctrl-C closes the viewer; capture continues.

Private reports: `/data/udm-iptv/diagnostics` (0600), including addresses and raw logs.

Export complete capture evidence locally; the original stays unchanged:

```sh
udm-iptv diagnose export /data/udm-iptv/diagnostics/CAPTURE.jsonl > share.jsonl
udm-iptv diagnose export /data/udm-iptv/diagnostics/CAPTURE.jsonl --format text > share.txt
```

See [diagnostics](docs/diagnostics.md) for retained evidence, collection limits and export formats.

Bounded journal events appear live; closing the viewer preserves capture.

See [telemetry details](docs/telemetry.md) for optional reliability reports.

See [startup troubleshooting](docs/troubleshooting.md) for DHCP and VLAN failures.

## Providers

[Provider profiles](docs/providers) supply editable defaults. [KPN specifications](docs/providers/kpn.md).

First setup suggests providers through ipify/PTR; confirm your selection.

Saved settings remain unchanged until you accept the review.

## Development

```sh
go test -race ./...
go vet ./...
go run ./cmd/udm-iptv preview
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=true -o dist/udm-iptv-linux-arm64 ./cmd/udm-iptv
goreleaser release --snapshot --clean --skip=publish
```

`preview` uses example data without system changes or telemetry.

CI builds binaries; routers need no Go toolchain.

## License

GPL-2.0-or-later. See [COPYING.txt](COPYING.txt).
