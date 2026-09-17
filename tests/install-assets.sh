#!/bin/sh
set -eu

UDM_IPTV_SOURCE_ONLY=1
# shellcheck disable=SC1091
. ./install.sh

assert_eq() {
	got=$1
	want=$2
	label=$3
	if [ "${got}" != "${want}" ]; then
		printf 'FAIL %s: got [%s] want [%s]\n' "${label}" "${got}" "${want}" >&2
		exit 1
	fi
}

v4_release='{
  "tag_name": "v4.3.1",
  "draft": false,
  "prerelease": false,
  "assets": [
    {"name": "udm-iptv_4.3.1_all.deb"},
    {"name": "udm-iptv_4.3.1.dsc"}
  ]
}'

v5_release='{
  "tag_name": "v5.0.0-preview.1",
  "draft": false,
  "prerelease": true,
  "assets": [
    {"name": "SHA256SUMS"},
    {"name": "udm-iptv-arm64.deb"},
    {"name": "udm-iptv-linux-arm64"}
  ]
}'

two_debs='{
  "tag_name": "v9.0.0",
  "assets": [
    {"name": "udm-iptv-arm64.deb"},
    {"name": "udm-iptv_9.0.0_all.deb"}
  ]
}'

dbgsym='{
  "tag_name": "v4.3.1",
  "assets": [
    {"name": "udm-iptv_4.3.1_all.deb"},
    {"name": "udm-iptv-dbgsym_4.3.1_all.deb"}
  ]
}'

assert_eq "$(printf '%s\n' "${v4_release}" | plausible_debs)" "udm-iptv_4.3.1_all.deb" "v4 deb"
assert_eq "$(printf '%s\n' "${v5_release}" | plausible_debs)" "udm-iptv-arm64.deb" "v5 deb"
assert_eq "$(release_deb_package v4.3.1 "${v4_release}")" "udm-iptv_4.3.1_all.deb" "pick v4"
assert_eq "$(release_deb_package v5.0.0-preview.1 "${v5_release}")" "udm-iptv-arm64.deb" "pick v5"
assert_eq "$(printf '%s\n' "${dbgsym}" | plausible_debs)" "udm-iptv_4.3.1_all.deb" "drop dbgsym"

if release_deb_package v9.0.0 "${two_debs}" >/dev/null 2>&1; then
	echo "FAIL two debs must fail closed" >&2
	exit 1
fi

releases='[
  {"tag_name": "v5.0.0-preview.1", "draft": false, "prerelease": true},
  {"tag_name": "v4.3.1", "draft": false, "prerelease": false}
]'

UDM_IPTV_PRERELEASE=true
assert_eq "$(printf '%s\n' "${releases}" | pick_latest_tag)" "v5.0.0-preview.1" "latest including pre"

UDM_IPTV_PRERELEASE=false
assert_eq "$(printf '%s\n' "${releases}" | pick_latest_tag)" "v4.3.1" "latest stable"

echo OK
