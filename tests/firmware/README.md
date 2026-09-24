# Firmware lifecycle tests

CI boots extracted UniFi OS filesystems using real systemd.

Models are discovered from published firmware tags. CI tests release, beta and
pinned pairs, sharing one run when multiple tracks select identical images.

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
- Fresh package setup and bootstrap from a legacy configuration backup.
- Provider-side DHCP fixture, RFC3442 routes and cleanup after stopping the service.

## Evidence

Every test writes evidence to `UDM_IPTV_FIRMWARE_EVIDENCE`, or
`dist/firmware-evidence` locally. CI uploads it on successful and failed runs.

Snapshots precede container stops, reboots and removal of package state. Cleanup
collects another snapshot before deleting containers and volumes. They retain
complete console output and journal records, container metadata, service and
network state, configuration, original diagnostic capture archives, runtime files
and DHCP fixture logs. Collector stderr and errors are recorded alongside any
partial output; one failed collector does not prevent subsequent collection or
container cleanup. Collection has a separate one-minute deadline per snapshot.

Docker command output is also retained verbatim, with arguments, timestamps and
failure details, including successful installer output. Raw archive and command
bytes are preserved without text re-encoding or line limits.

## Boundaries

Docker shares the runner kernel; firmware kernels remain untested.

Hardware ports and static IPTV addresses are test fixtures.

The real provider network, television playback and physical switches remain untested.

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
- Model and board names receive separate version/latest aliases. Historical
  lower-case platform names (`udmprose`, `udmea4c`) remain updated alongside
  `udmse` and `udmbeast`.
- Global `latest` tracks UDM Pro; existing versions remain.
