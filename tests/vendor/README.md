# Yo! <!-- rumdl-disable-line no-trailing-punctuation -->

Captured responses from Ubiquiti's two firmware APIs. [`refresh.sh`] fetches them all:

```bash
UBIC_AUTH='<the UBIC_AUTH cookie>' ./refresh.sh
```

Every response is reshaped by [`shape.jq`], formatted by dprint, and written only if it passes its soundness check. [`describe.jq`] renders the line each one logs, [`lookup.jq`] turns a slug back into an id.

## `fw-catalog.json`

The whole firmware catalogue: 11776 records over 231 products, channels `release`, `beta-public`, `release-cn` and `lts`. [`.github/actions/unifi-os-matrix`][unifi-os-matrix] builds its job list from `._embedded.firmware[]`, and each record carries `platform`, `version_major/minor/patch`, `created`, `sha256_checksum` and a download URL under `._links.data.href`.

`limit` is honoured up to any value and the envelope has no pagination cursor, so one request returns everything.

```bash
./refresh.sh catalog
```

The endpoint serves the same body signed in as signed out, and it indexes no UniFi OS Early Access build. [`pinned.json`] names those by hand.

## [`ui-releases.json`], [`ui-dream-machines/`]

`community.svc.ui.com` is the Apollo endpoint behind community.ui.com. Early Access releases are announced there, and it hands out a download URL per board without a checksum. `releases.graphql` and `release.graphql` hold the two queries; `jq` wraps one of them with its variables into the request body, because a newline inside the JSON string holding the query is answered with `{"error":"Something went wrong. Please try again"}`.

`UBIC_AUTH` holds the cookie of a signed-in community.ui.com session, or `UBIC_AUTH_FILE` names a file holding it. A bare `./refresh.sh` without either still refreshes the catalog and leaves these two alone. A cookie without release-testing access does not fail: the request returns 100 sound-looking items with every `T` stage dropped, and with them every 6.0.x release, so the index is only written when it carries at least one `T`. Introspection is disabled.

[`ui-releases.json`] is the release index, grouped by product family, newest version first inside each, one entry per version and stage:

```json
"UniFi-OS-Dream-Machines": [
	{ "version": "6.0.7", "stage": "GA", "id": "439a4756-3fa0-4529-ba89-719ae900c752" },
	{ "version": "5.1.30", "stage": "RC", "id": "ef80a242-8e01-446e-a678-41cb122cbb1f" }
]
```

```bash
./refresh.sh index
```

The family is the slug with its version stripped off the end, taken from the `version` field rather than matched as trailing digits, which keeps `UniFi-OS-Express-7-5-2-10` under `UniFi-OS-Express-7` instead of folding it into `UniFi-OS-Express`. Some releases come back twice under two ids, identical down to their download links; one of each pair survives.

Each release is then fetched by slug for its download links. The subdirectory is named after the product family and the file after the version, so `UniFi-OS-Dream-Machines-6-0-7` lands in [`ui-dream-machines/6-0-7.json`]:

```bash
./refresh.sh release UniFi-OS-Dream-Machines-6-0-7
```

A bare [`./refresh.sh`][`refresh.sh`] re-fetches every release already on disk by the id stored in it, so new families only ever need the command above once.

Each entry in `links[]` is titled with its board (`UDM`, `UDM-Pro`, `UDM-SE`, `UDM-Pro-Max`, `UDM-Beast`) and points at the `.bin` on `fw-download.ubnt.com`. The `sha256` in `pinned.json` comes from downloading those files and hashing them. This query needs no cookie at all; it answers the same signed out.

[`ui-releases.json`]: ui-releases.json
[`ui-dream-machines/`]: ui-dream-machines/
[`refresh.sh`]: refresh.sh
[`ui-dream-machines/6-0-7.json`]: ui-dream-machines/6-0-7.json
[`pinned.json`]: ../../.github/actions/unifi-os-matrix/pinned.json
[unifi-os-matrix]: ../../.github/actions/unifi-os-matrix/
[`shape.jq`]: shape.jq
[`describe.jq`]: describe.jq
[`lookup.jq`]: lookup.jq
