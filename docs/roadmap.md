# Roadmap

Updated: 15 September 2026. Branch: kjanat/udm-iptv#38.

Implemented locally. CI and router verification remain separate.

## What does this mean for TV viewing?

The reported TV freezes still have no confirmed diagnosis.

## Implemented locally; CI verification pending

- [x] Preserve unchanged IPTV DHCP routes during renewal.
      IPTV leases are separate from your public internet address.
      Invalid leases and failed updates have regression tests.
      Background: fabianishere/udm-iptv#57.
- [x] Verify stable processes and automatic startup before reporting success.
      Program readiness does not prove successful TV playback.
      Background: fabianishere/udm-iptv#420.
- [x] Show proxy, IGMP version, quickleave and debug settings.
      Background: fabianishere/udm-iptv#410.

## Wizard and downstream checks

- [x] **Expose downstream state and diagnostic blind spots.**
      Local bridge snooping and querier settings are collected.
      Unavailable switch firmware/settings remain explicitly “not checked”.
      Native proxy and TV playback remain “not checked”.
      Regression tests: `internal/diagnostics/downstream_test.go`.
      Background: fabianishere/udm-iptv#408 and fabianishere/udm-iptv#247.
- [x] **Suggest providers using bounded public-IP/PTR discovery.**
      First interactive setup only; suggestions require user confirmation.
      Saved settings, explicit profiles and previews remain untouched.
      Unknown providers remain manual; network-reporting opt-out prevents discovery.
      Regression tests: `internal/cli/provider_test.go`, `internal/ui/review_test.go`.
- [x] **Validate fields and review settings before saving.**
      Cancelled reviews preserve the previous configuration.
- [x] **Allow existing addresses for untagged provider profiles.**

## Review follow-up

- [x] Preserve proxy binaries and libraries across firmware erasure.
      Saved runtimes replace missing system proxies without downloads.
      Regression tests: `internal/runtimebundle`; isolated-filesystem test runs in CI.
- [x] Authenticate private release assets without leaking tokens.
- [x] Preserve legacy untagged DHCP and proxy defaults.
- [x] Batch journal writes; propagate storage failures.
- [x] Provide repository context when publishing releases.
- [x] Clear the configured lint backlog.

## Already addressed in the Go code

- Quickleave can be configured for both proxy programs.

## Verification gaps

- [x] Restore firmware-container installation, boot, reboot and upgrade tests.
      CI wiring restored; the first remote run remains pending.
      Namespace and chroot tests do not replace firmware coverage.
- [ ] Verify preserved runtimes using real firmware proxy binaries.
      The current chroot test uses a small substitute executable.

## No settings changes needed just to follow this list

Quickleave controls how quickly an old stream stops.

Multiple receivers may need different settings. Avoid blind changes.

Checked items describe code changes, not confirmed freeze fixes.

These changes have not been installed on the household router.
