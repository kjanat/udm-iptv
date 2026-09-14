# udm-iptv

Routed IPTV for UniFi OS, implemented as one statically linked Go binary.

The binary owns the complete lifecycle: interactive configuration, persistent installation, multicast proxy supervision, DHCP route handling, upgrades, health checks, diagnostics, and shell completion. It installs itself under `/data`, so UniFi OS firmware updates do not remove the running implementation. A systemd unit and tmpfiles rule under `/etc` reconnect the persistent binary on every boot.

## Install

All supported UniFi consoles use ARM64:

```console
curl -fLO https://github.com/kjanat/udm-iptv/releases/download/v<VERSION>/udm-iptv_<VERSION>_arm64.deb
sudo apt install ./udm-iptv_<VERSION>_arm64.deb
```

The Debian package asks for the provider profile through debconf, detects the console interfaces, installs the persistent service, and verifies that it becomes healthy. The standalone binary remains available as an alternative:

```console
curl -fLO https://github.com/kjanat/udm-iptv/releases/latest/download/udm-iptv-linux-arm64
curl -fLO https://github.com/kjanat/udm-iptv/releases/latest/download/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
chmod +x udm-iptv-linux-arm64
sudo ./udm-iptv-linux-arm64 install
```

The installer opens an interactive terminal form, writes a typed configuration to `/data/udm-iptv/config.json`, installs the persistent binary, enables the service, and waits until the proxy has remained healthy beyond its restart window.

Existing installations using `/etc/udm-iptv.conf` are imported without executing the shell file. Once the new service is healthy, obsolete offline-restore artifacts are removed by the installer.

## Commands

```console
udm-iptv configure
udm-iptv status
udm-iptv diagnose
udm-iptv diagnose --capture 30m --format both --follow
udm-iptv restart
udm-iptv upgrade
udm-iptv uninstall
```

`configure` uses a Bubble Tea/Huh form with explanations for every networking choice. The same values can be supplied as flags with `--non-interactive` for automation.

`upgrade` resolves a GitHub release, downloads the matching architecture-specific binary and `SHA256SUMS`, verifies the checksum, atomically replaces the persistent executable, restarts the service, and verifies stability.

## Diagnostics

A one-time `diagnose` shows the service, proxy, IPTV link, routes, and multicast state. A bounded capture records an initial snapshot, periodic samples, service logs, a final snapshot, and an explicit completion marker.

`--follow` opens a live Bubble Tea view. `q` or Ctrl-C closes only the viewer; the detached capture continues until its displayed completion time. Captures are written with mode `0600` under `/data/udm-iptv/diagnostics`.

Share-ready output excludes configured MACs and static addresses. Device-assigned, private, shared, loopback, link-local, multicast, and IPv6 addresses; common MAC formats; and the router hostname are redacted. Public provider route prefixes remain visible because they are needed to diagnose routed IPTV. Review the file before posting it publicly.

## Configuration model

NAT destinations and multicast proxy source ranges are separate settings. This avoids the legacy ambiguity where one `WAN_RANGES` value both selected unicast destinations for masquerading and controlled `igmpproxy` source acceptance.

Provider profiles are starting points. The form always exposes the generated values for review before saving them.

Provider-specific sources and implementation notes are documented under [`docs/providers`](docs/providers), including the [KPN fibre specification](docs/providers/kpn.md).

## Development

```console
go test ./...
go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/udm-iptv-linux-arm64 ./cmd/udm-iptv
goreleaser release --snapshot --clean --skip=publish
```

No Go compiler or module cache is installed on the router. CI produces the static ARM64 release binary.

## License

GPL-2.0-or-later. See [COPYING.txt](COPYING.txt).
