# Roadmap

Status: 14 September 2026. This list covers the Go implementation in kjanat/udm-iptv#38.
Changes in that branch are not automatically installed on the router.

## What does this mean for TV viewing?

The inspected router was running udm-iptv without duplicate IPTV routes.
That does not prove uninterrupted TV playback. The cause of the reported
freezes has not been established.

## Implemented locally; CI verification pending

- [x] **Avoid removing working routes during an unchanged TV lease renewal.**
      This is the DHCP lease on VLAN 4, not the public internet address.
      The lease is renewed even when the TV address stays the same.
      New lease data is checked before changing the network. Routes are
      applied before obsolete routes are removed; unchanged routes stay put.
      Tests cover invalid data, changed leases and failed route updates.
      An isolated Linux network test is included in CI. Background: fabianishere/udm-iptv#57.
- [x] **Check that udm-iptv keeps running and is enabled to start after reboot.**
      Throughout the observation period and at its end, check automatic
      startup, blocked services, running processes and restarts. Then report
      “udm-iptv has started”. This checks the program on the router;
      it does not verify that a TV receiver gets a picture. Background: fabianishere/udm-iptv#420.
- [x] **Include the missing settings in the readable diagnostics.**
      Text reports and capture snapshots now show the IGMP version and
      whether quickleave and debug logging are enabled. Background: fabianishere/udm-iptv#410.

## Improve next

- [ ] **Check the network beyond the router.**
      Switches can block TV traffic even when the router is working.
      Collect switch firmware and multicast settings where available.
      Otherwise, explicitly report “not checked”. Background: fabianishere/udm-iptv#408 and fabianishere/udm-iptv#247.
- [ ] **Suggest the provider automatically.**
      A live network check identified KPN through the public IP's reverse-DNS
      hostname (PTR) and network registration (ASN). This still needs to be
      implemented. Explain the suggestion and let the user change it.
      Keep personal IP addresses out of this documentation.

## Already addressed in the Go code

- Quickleave can be configured for both proxy programs.

## No settings changes needed just to follow this list

Quickleave controls how quickly an old TV stream is stopped. Multiple
receivers can affect the appropriate setting. Changing quickleave or IGMP
versions blindly is not a proven fix for the reported freezes.

Checked items describe code changes, not a confirmed fix for the reported
freezes. They have not been installed or tested on the household router.
