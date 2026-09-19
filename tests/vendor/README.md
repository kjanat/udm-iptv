# Yo! <!-- rumdl-disable-line no-trailing-punctuation -->

Captured responses from Ubiquiti's two firmware APIs. [`refresh.sh`] fetches them all:

```bash
UBIC_COOKIE_FILE=~/.config/ubic.cookie ./refresh.sh
```

Every response is formatted by dprint on the way in, and one without a `data` member is reported instead of written.

## `fw-catalog.json`

The whole firmware catalogue: 11776 records over 231 products, channels `release`, `beta-public`, `release-cn` and `lts`. [`.github/actions/unifi-os-matrix`][unifi-os-matrix] builds its job list from `._embedded.firmware[]`, and each record carries `platform`, `version_major/minor/patch`, `created`, `sha256_checksum` and a download URL under `._links.data.href`.

`limit` is honoured up to any value and the envelope has no pagination cursor, so one request returns everything.

```bash
./refresh.sh catalog
```

The endpoint serves the same body signed in as signed out, and it indexes no UniFi OS Early Access build. [`pinned.json`] names those by hand.

## [`ui-releases.json`], [`ui-dream-machines/`]

`community.svc.ui.com` is the Apollo endpoint behind community.ui.com. Early Access releases are announced there, and it hands out a download URL per board without a checksum. `releases.graphql` and `release.graphql` hold the two queries; `jq` wraps one of them with its variables into the request body, because a newline inside the JSON string holding the query is answered with `{"error":"Something went wrong. Please try again"}`.

`UBIC_COOKIE_FILE` names a file holding the `UBIC_AUTH` cookie of a signed-in community.ui.com session. An anonymous request still succeeds and returns an index with every beta and RC removed, so a refresh without the cookie silently shrinks these files. Introspection is disabled.

[`ui-releases.json`] is the release index, newest first, where `stage` is `GA`, `RC` or `T`:

```bash
./refresh.sh index
```

Each release is then fetched by slug for its download links. The subdirectory is named after the product family and the file after the version, so `UniFi-OS-Dream-Machines-6-0-7` lands in [`ui-dream-machines/6-0-7.json`]:

```bash
./refresh.sh release UniFi-OS-Dream-Machines-6-0-7
```

A bare [`./refresh.sh`][`refresh.sh`] re-fetches every release already on disk by the id stored in it, so new families only ever need the command above once.

Each entry in `links[]` is titled with its board (`UDM`, `UDMPRO`, `UDMPROSE`, `UDMPROMAX`, `UDMEA4C`) and points at the `.bin` on `fw-download.ubnt.com`. The `sha256` in `pinned.json` comes from downloading those files and hashing them.

[`ui-releases.json`]: ui-releases.json
[`ui-dream-machines/`]: ui-dream-machines/
[`refresh.sh`]: refresh.sh
[`ui-dream-machines/6-0-7.json`]: ui-dream-machines/6-0-7.json
[`pinned.json`]: ../../.github/actions/unifi-os-matrix/pinned.json
[unifi-os-matrix]: ../../.github/actions/unifi-os-matrix/
