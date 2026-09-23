# Startup troubleshooting

## Diagnostic capture status

Each capture keeps a private `.status.json` file alongside its text or JSONL output.
It records the worker's start time and deadline, then its completed, timed-out or
failed outcome. The launcher reports that acknowledgement; a clean process exit
without a completion record is an error. A capture that finishes before the launcher
returns is reported as completed, with no new completion deadline.

## DHCP acquisition

DHCP has 30 seconds to apply a lease. Defaults (`-t 3 -T 3 -A 2`) allow two complete
discovery rounds; custom options do not extend the deadline. `leasefail` and `nak` are
retry warnings. On timeout, check client logs for OFFERs, ACKs and hook failures, then
verify the interface, VLAN and provider options.

## Existing VLAN on another parent

Run:

```sh
udm-iptv diagnose
```

The report compares the configured VLAN interface with the router's actual parent and
VLAN ID, and explains any mismatch. Confirm which physical port carries your provider's
IPTV connection before correcting settings with `udm-iptv configure`.

Existing VLANs without the `udm-iptv` ownership marker are also refused, even when
the parent and VLAN ID match. Debian upgrades from v4 transfer the matching legacy
VLAN as part of installation; other existing interfaces are not adopted.
Choose an unused IPTV interface name, or establish who owns the existing interface
before migrating it. Untagged interfaces retain unrelated addresses and DHCP routes;
collisions with resources absent from the recorded lease are refused.

## Upgrade from v4

On v4.3.2:

```sh
udm-iptv upgrade --prerelease
```

On an older v4 version, first run `udm-iptv upgrade` to obtain the current v4
installer, then run the command above.

The package upgrade saves the old configuration and stops the v4 service before
replacing its files, allowing v4 to remove its NAT rules. v5 imports the settings
and takes over the old IPTV VLAN only when its name, parent and VLAN ID match both
the saved v4 configuration and the imported v5 configuration. No manual VLAN
deletion or ownership marking is needed for that matching configuration.

If those values differ, installation reports the mismatch and leaves that
interface unchanged. Check `udm-iptv diagnose` and the configured WAN interface
before retrying. The saved handover configuration is retained until installation
passes its health check.

## Preview upgrades

On `5.0.0-preview.5`, use `udm-iptv upgrade --prerelease`. Newer previews select that
channel automatically. Legacy stable packages are not upgrade targets for the Go
preview.
