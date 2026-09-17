# udm-iptv

Routed IPTV for UniFi OS, persistent across firmware updates.

## Install

Install the latest release on the console:

```sh
curl -fLO https://github.com/kjanat/udm-iptv/releases/latest/download/udm-iptv-arm64.deb
sudo apt install ./udm-iptv-arm64.deb
```

Installation verifies stable proxy readiness before reporting success.

<details>
<summary>Standalone binary</summary>

```sh
curl -fLO https://github.com/kjanat/udm-iptv/releases/latest/download/udm-iptv-linux-arm64
curl -fLO https://github.com/kjanat/udm-iptv/releases/latest/download/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
chmod +x udm-iptv-linux-arm64
sudo ./udm-iptv-linux-arm64 install
```

`install --dry-run` previews without root, changes, telemetry or health checks.

`--non-interactive` skips prompts. `apt install` performs real installation.

</details>

Configuration: `/data/udm-iptv/config.json`. Legacy settings migrate automatically.

Missing system proxies use preserved binaries and libraries offline.

## Commands

```sh
udm-iptv configure
udm-iptv status
udm-iptv diagnose
udm-iptv restart
udm-iptv upgrade
udm-iptv uninstall
```

See `udm-iptv <command> --help` for options.

## Diagnostics

Record diagnostics for 30 minutes:

```sh
udm-iptv diagnose --capture 30m --format both --follow
```

`q` or Ctrl-C closes the viewer; capture continues.

Reports: `/data/udm-iptv/diagnostics`. Review before sharing; provider prefixes remain visible.

See [telemetry details](docs/telemetry.md) for optional reliability reports.

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

 <!-- markdownlint-disable-file line-length no-inline-html -->
