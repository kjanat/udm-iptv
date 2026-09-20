#!/usr/bin/env bash
set -euo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "${work}"' EXIT
mkdir "${work}/bin"
export FIXTURE_LOG="${work}/commands"
export FIXTURE_STATE="${work}/state"

cat >"${work}/bin/ip" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"${FIXTURE_LOG}"
case "$*" in
    '-oneline link show iptv')
        # Exercise waiting for the package to create its interface.
        if [ ! -e "${FIXTURE_STATE}" ]; then
            touch "${FIXTURE_STATE}"
            exit 1
        fi
        echo '14: iptv@eth8: <BROADCAST,MULTICAST,UP> mtu 1500'
        ;;
    '-details link show iptv') echo '    vlan protocol 802.1Q id 4 <REORDER_HDR>' ;;
    'link show iptv-peer') exit 0 ;;
    *) ;;
esac
EOF
cat >"${work}/bin/dnsmasq" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"${FIXTURE_LOG}"
exit 23
EOF
chmod +x "${work}/bin/ip" "${work}/bin/dnsmasq"

status=0
PATH="${work}/bin:${PATH}" sh "${repo}/docker/unifi-os/fixtures.sh" dhcp || status=$?
[[ ${status} == 23 ]]
grep -Fqx 'link delete iptv-peer' "${FIXTURE_LOG}"
grep -Fqx 'link add link peer-eth8 name iptv-peer type vlan id 4' "${FIXTURE_LOG}"
grep -Fqx 'address replace 198.51.100.1/24 dev iptv-peer' "${FIXTURE_LOG}"
grep -Fq -- '--keep-in-foreground --conf-file=/dev/null' "${FIXTURE_LOG}"
grep -Fq -- '--log-facility=- --log-dhcp' "${FIXTURE_LOG}"
echo 'DHCP fixture waits for the VLAN and propagates server failures'

# Run the actual unit-generation block, but redirect its runtime paths into
# the temporary directory. No host system units or network state are changed.
sed -n '/^mkdir -p .*test_target.*requires/,/^# The extracted root/{ /^# The extracted root/d; p; }' \
	"${repo}/docker/unifi-os/harness.sh" \
	| sed "s|/run/systemd/system|${work}/units|g; s|/fixtures.sh|${repo}/docker/unifi-os/fixtures.sh|g" \
		>"${work}/units.sh"
test_target=udm-iptv-test.target UDM_IPTV_TEST_LOCK_SECONDS=2 sh -eu "${work}/units.sh"
grep -Fqx 'Type=notify' "${work}/units/udm-iptv-test-lock.service"
grep -Fqx 'Before=udm-iptv-restore.service' "${work}/units/udm-iptv-test-lock.service"
test -L "${work}/units/udm-iptv-test.target.requires/udm-iptv-test-dhcp.service"
test -L "${work}/units/udm-iptv-test.target.requires/udm-iptv-test-lock.service"
systemd-analyze --user verify "${work}/units/udm-iptv-test-dhcp.service" "${work}/units/udm-iptv-test-lock.service"
echo 'Fixture units validate, with lock readiness ordered before restore'

# Optional native-systemd check. Redirect only the test lock path; retain the
# real notification and sleep implementation. This never opens dpkg's lock.
if systemctl --user show --property=Version >/dev/null 2>&1; then
	sed "s|/var/lib/dpkg/lock|${work}/dpkg-lock|" \
		"${repo}/docker/unifi-os/fixtures.sh" >"${work}/fixtures.sh"
	systemd-run --user --quiet --collect --wait --pipe \
		--property=Type=notify --property=NotifyAccess=all \
		--property=TimeoutStartSec=5 \
		/bin/sh "${work}/fixtures.sh" lock 2
	test -e "${work}/dpkg-lock"
	echo 'Lock helper signals readiness and completes under real systemd'
else
	echo 'SKIP: native systemd notification test (no user manager)'
fi

# Exercise the real initial-install capture block with a failing docker stub.
sed -n '/^install_status=0/,/^if grep -Fq .*Illegal number/{ /^if grep -Fq .*Illegal number/d; p; }' \
	"${repo}/docker/unifi-os/test-install.sh" >"${work}/capture.sh"
docker() {
	echo 'installer diagnostic retained' >&2
	return 42
}
report_error() { echo "$*" >&2; }
export -f docker report_error
status=0
output=$(from_name=test-container bash -eu "${work}/capture.sh" 2>&1) || status=$?
[[ ${status} == 42 ]]
grep -Fq 'installer diagnostic retained' <<<"${output}"
grep -Fq 'initial installation failed in test-container (exit 42)' <<<"${output}"
echo 'Installer diagnostics and original failure status are retained'

# Exercise release lookup without network access or real credentials.
sed -n '/^fetch_v5_package() {/,/^}/p' \
	"${repo}/docker/unifi-os/test-install.sh" >"${work}/fetch.sh"
# shellcheck source=/dev/null
source "${work}/fetch.sh"
gh() {
	[[ $1 == api && $2 == 'repos/example/iptv/releases?per_page=30' ]]
	if [[ ${lookup_status} != 0 ]]; then
		return "${lookup_status}"
	fi
	printf '%s\n' "${release_data}"
}
curl() {
	printf '%s\n' "$*" >"${work}/download"
}
export GITHUB_REPOSITORY=example/iptv
v5_deb="${work}/v5.deb"
lookup_status=0
release_data='[{"draft":false,"prerelease":false,"assets":[]},{"draft":true,"prerelease":true,"assets":[]},{"draft":false,"prerelease":true,"assets":[{"name":"checksums.txt","browser_download_url":"https://example.invalid/checksums"},{"name":"v5.deb","browser_download_url":"https://example.invalid/v5.deb"}]}]'
fetch_v5_package
grep -Fq -- "-o ${v5_deb}" "${work}/download"
grep -Fq -- '--retry 3 --connect-timeout 30 --max-time 300' "${work}/download"
grep -Fq 'https://example.invalid/v5.deb' "${work}/download"
rm "${work}/download"
lookup_status=17
status=0
fetch_v5_package || status=$?
[[ ${status} == 17 && ! -e ${work}/download ]]
lookup_status=0
release_data='[]'
status=0
fetch_v5_package || status=$?
[[ ${status} == 1 && ! -e ${work}/download ]]
echo 'Release lookup uses gh, propagates API failures, and rejects missing assets'
