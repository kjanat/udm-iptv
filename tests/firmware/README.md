# Firmware lifecycle tests

CI boots extracted UniFi OS filesystems using real systemd.

Five models use their two newest published stable versions.

## Coverage

- Debian installation and package-version upgrade.
- Installed binary matches the shared build artifact exactly.
- Real improxy readiness and stable process observation.
- Failed installation cannot report successful startup.
- Automatic startup after reboot.
- Offline firmware replacement preserving `/data` and `/etc` changes.
- Missing firmware proxy uses the preserved real executable.
- Diagnostic snapshots, bounded captures and file permissions.
- Removal, retained configuration, standalone reinstall and purge.

## Boundaries

Docker shares the runner kernel; firmware kernels remain untested.

Hardware ports and static IPTV addresses are test fixtures.

Provider DHCP, television playback and physical switches remain untested.

Privileged execution requires the `firmware` build tag.

The bootstrap never installs, enables or starts udm-iptv itself.

CI resolves version pairs before starting the model matrix.

## Image refresh

`cmd/firmware` owns image orchestration; Docker operations use its SDK.

The default track is `release`. Pass `--track beta` consistently to `catalog`,
`build`, `publish`, and `published` to include beta and release-candidate versions.
Beta version tags include `-beta-` (for example `udmpro-beta-5.1.0`), so
stable selection cannot pick a beta-channel build with a plain version number.
Beta publication updates `beta` aliases, preserving stable `latest` aliases.

- Daily at 03:17 UTC; manual runs support model selection.
- Ubiquiti's catalog supplies two stable versions per model.
- Matching fingerprints reuse images; downloads require checksum verification.
- One package build supplies every installation, reboot, recovery test.
- Successful tests permit publication; remote image IDs are verified.
- Model and board names receive separate version/latest aliases.
- Global `latest` tracks UDM Pro; existing versions remain.
