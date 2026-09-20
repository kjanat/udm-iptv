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

got=$(printf '%s\n' "${v4_release}" | plausible_debs)
assert_eq "${got}" "udm-iptv_4.3.1_all.deb" "v4 deb"

got=$(printf '%s\n' "${v5_release}" | plausible_debs)
assert_eq "${got}" "udm-iptv-arm64.deb" "v5 deb"

got=$(release_deb_package v4.3.1 "${v4_release}")
assert_eq "${got}" "udm-iptv_4.3.1_all.deb" "pick v4"

got=$(release_deb_package v5.0.0-preview.1 "${v5_release}")
assert_eq "${got}" "udm-iptv-arm64.deb" "pick v5"

got=$(printf '%s\n' "${dbgsym}" | plausible_debs)
assert_eq "${got}" "udm-iptv_4.3.1_all.deb" "drop dbgsym"

# Two candidates are ambiguous, and the installer must refuse rather than guess.
set +e
release_deb_package v9.0.0 "${two_debs}" >/dev/null 2>&1
status=$?
set -e
if [ "${status}" -eq 0 ]; then
	echo "FAIL two debs must fail closed" >&2
	exit 1
fi

releases='[
  {"tag_name": "v5.0.0-preview.1", "draft": false, "prerelease": true},
  {"tag_name": "v4.3.1", "draft": false, "prerelease": false}
]'

UDM_IPTV_PRERELEASE=true
got=$(printf '%s\n' "${releases}" | pick_latest_tag)
assert_eq "${got}" "v5.0.0-preview.1" "latest including pre"

UDM_IPTV_PRERELEASE=false
got=$(printf '%s\n' "${releases}" | pick_latest_tag)
assert_eq "${got}" "v4.3.1" "latest stable"

# v4 runs the proxy as the service; v5 runs a supervisor that owns it.
marker_dir=$(mktemp -d)
marker="${marker_dir}/go-package"
UDM_IPTV_GO_MARKER="${marker}"

got=$(printf '%s\n' /usr/bin/igmpproxy -n /run/udm-iptv/proxy.conf | service_process)
assert_eq "${got}" "/usr/bin/igmpproxy" "igmpproxy as the service"

got=$(printf '%s\n' /usr/bin/improxy -c /run/udm-iptv/proxy.conf | service_process)
assert_eq "${got}" "/usr/bin/improxy" "improxy as the service"

daemon_arguments() {
	printf '%s\n' /data/udm-iptv/bin/udm-iptv daemon --config /data/udm-iptv/config.json
}

got=$(daemon_arguments | service_process)
assert_eq "${got}" "unknown" "supervisor without the marker"

: >"${marker}"
got=$(daemon_arguments | service_process)
assert_eq "${got}" "/data/udm-iptv/bin/udm-iptv" "supervisor with the marker"

got=$(printf '%s\n' /usr/sbin/sshd -D | service_process)
assert_eq "${got}" "unknown" "an unrelated process"

rm -f "${marker}"
rmdir "${marker_dir}"

echo OK
