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
the parent and VLAN ID match. This includes VLANs created by older releases.
Choose an unused IPTV interface name, or establish who owns the existing interface
before migrating it. Untagged interfaces retain unrelated addresses and DHCP routes;
collisions with resources absent from the recorded lease are refused.

## Preview upgrades

On `5.0.0-preview.5`, use `udm-iptv upgrade --prerelease`. Current source selects that
channel automatically. These fixes require a newer published preview; legacy stable
packages are not upgrade targets for the Go preview.
