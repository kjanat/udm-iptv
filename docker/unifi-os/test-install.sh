#!/usr/bin/env bash
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd -- "${here}/../.." && pwd)
sku="${1:-}"
deb="${2:-}"

if [[ -z ${sku} || -z ${deb} ]]; then
	echo "usage: $0 <sku> <deb>" >&2
	exit 1
fi

if [[ -n ${FROM_IMAGE:-} && -n ${TO_IMAGE:-} ]]; then
	from_image=${FROM_IMAGE}
	to_image=${TO_IMAGE}
else
	pair=$("${here}/upgrade-pair.sh" "${sku}")
	from_image=
	to_image=
	while IFS='=' read -r name value; do
		case ${name} in
			from_image) from_image=${value} ;;
			to_image) to_image=${value} ;;
			*) ;;
		esac
	done <<<"${pair}"
	if [[ -z ${from_image} || -z ${to_image} ]]; then
		echo "error: upgrade pair did not return both images" >&2
		exit 1
	fi
fi
deb=$(readlink -f "${deb}")
work=$(mktemp -d)
old_root="${work}/old"
old_deb=${OLD_PACKAGE:-"${work}/udm-iptv-old.deb"}
current_version=${CURRENT_PACKAGE_VERSION:-}
old_version=${OLD_PACKAGE_VERSION:-}
id="udm-iptv-${sku}-$$"
vol_data="${id}-data"
from_name="${id}-from"
to_name="${id}-to"
group_open=0

workflow_escape() {
	local value=$1
	value=${value//%/%25}
	value=${value//$'\r'/%0D}
	value=${value//$'\n'/%0A}
	printf '%s' "${value}"
}

workflow_emit() {
	local command=$1
	local message=$2
	local escaped
	if [[ ${GITHUB_ACTIONS:-} == true ]]; then
		escaped=$(workflow_escape "${message}")
		printf '::%s::%s\n' "${command}" "${escaped}"
	fi
}

workflow_error() {
	local message=$1
	local escaped
	if [[ ${GITHUB_ACTIONS:-} == true ]]; then
		escaped=$(workflow_escape "${message}")
		printf '::error file=docker/unifi-os/test-install.sh,title=UniFi OS integration test::%s\n' \
			"${escaped}"
	fi
}

report_error() {
	local message=$1
	workflow_error "${message}"
	echo "error: ${message}" >&2
}

group_end() {
	if ((group_open)); then
		workflow_emit endgroup ""
		group_open=0
	fi
}

group_begin() {
	local title=$1
	group_end
	if [[ ${GITHUB_ACTIONS:-} == true ]]; then
		workflow_emit group "${title}"
	else
		printf '\n==> %s\n' "${title}"
	fi
	group_open=1
}

cleanup() {
	local status=$?
	trap - EXIT
	group_end
	docker rm -f "${from_name}" "${to_name}" >/dev/null 2>&1 || true
	docker volume rm "${vol_data}" >/dev/null 2>&1 || true
	rm -rf "${work}"
	exit "${status}"
}
trap cleanup EXIT

dump() {
	local name=$1
	local status
	group_begin "Diagnostics: ${name}"
	status=$(docker inspect -f '{{.State.Status}}' "${name}" 2>/dev/null) || status=gone
	echo "container status=${status}" >&2
	docker logs "${name}" >&2 || true
	docker exec "${name}" systemctl is-system-running >&2 || true
	docker exec "${name}" systemctl list-jobs --no-pager >&2 || true
	docker exec "${name}" systemctl show \
		-p Id -p ActiveState -p SubState -p Job \
		udm-iptv-test.target network.target network-online.target >&2 || true
	docker exec "${name}" systemctl status udm-iptv-restore.service udm-iptv.service >&2 || true
	docker exec "${name}" journalctl -u udm-iptv-restore -u udm-iptv --no-pager >&2 || true
	group_end
}

ensure_arm64() {
	local image=$1
	if docker run --rm --platform linux/arm64 "${image}" uname -m; then
		return 0
	fi
	docker run --privileged --rm tonistiigi/binfmt --install arm64
	docker run --rm --platform linux/arm64 "${image}" uname -m
}

boot() {
	local name=$1
	local image=$2
	docker rm -f "${name}" >/dev/null 2>&1 || true
	docker run -d --name "${name}" --platform linux/arm64 --privileged --cgroupns=host \
		--stop-signal SIGRTMIN+3 \
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw \
		--tmpfs /run:exec --tmpfs /run/lock --tmpfs /tmp:exec \
		-v "${vol_data}:/data" \
		-v "${deb}:/tmp/udm-iptv.deb:ro" \
		-v "${old_deb}:/tmp/udm-iptv-old.deb:ro" \
		-v "${repo}/install.sh:/tmp/install.sh:ro" \
		-v "${here}/harness.sh:/harness.sh:ro" \
		-v "${here}/udm-iptv-test.target:/etc/systemd/system/udm-iptv-test.target:ro" \
		-e DEBIAN_FRONTEND=noninteractive \
		-e UDM_IPTV_PACKAGE=/tmp/udm-iptv.deb \
		"${image}" \
		/harness.sh
}

wait_systemd() {
	local name=$1
	local n=0
	local status
	while ((n < 60)); do
		status=$(docker inspect -f '{{.State.Status}}' "${name}" 2>/dev/null || echo gone)
		if [[ ${status} == exited || ${status} == dead || ${status} == gone ]]; then
			report_error "${name} ${status} while waiting for the test target"
			dump "${name}"
			return 1
		fi
		if [[ ${status} == running ]] \
			&& docker exec "${name}" test -S /run/systemd/private \
			&& docker exec "${name}" systemctl is-active --quiet udm-iptv-test.target 2>/dev/null; then
			echo "test target active in ${name}"
			return 0
		fi
		if ((n % 10 == 0)); then
			workflow_emit debug "Waiting for the test target in ${name}: container=${status}, elapsed=${n}s"
		fi
		sleep 2
		((n += 2))
	done
	report_error "test target did not become active in ${name}"
	dump "${name}"
	return 1
}

wait_pkg() {
	local name=$1
	local n=0
	while ((n < 180)); do
		if docker exec "${name}" test -e /usr/bin/udm-iptv \
			&& docker exec "${name}" test -e /usr/lib/udm-iptv/udm-iptvd; then
			echo "package present in ${name}"
			return 0
		fi
		if ((n % 10 == 0)); then
			workflow_emit debug "Waiting for the restored package in ${name}: elapsed=${n}s"
		fi
		sleep 2
		((n += 2))
	done
	report_error "restore did not install the package in ${name}"
	dump "${name}"
	return 1
}

wait_active() {
	local name=$1
	local n=0
	docker exec "${name}" systemctl start --no-block udm-iptv
	while ((n < 180)); do
		if docker exec "${name}" systemctl is-enabled --quiet udm-iptv 2>/dev/null \
			&& docker exec "${name}" systemctl is-active --quiet udm-iptv 2>/dev/null; then
			docker exec "${name}" systemctl is-enabled udm-iptv
			docker exec "${name}" systemctl is-active udm-iptv
			return 0
		fi
		if ((n % 10 == 0)); then
			workflow_emit debug "Waiting for udm-iptv in ${name}: elapsed=${n}s"
		fi
		sleep 2
		((n += 2))
	done
	report_error "udm-iptv is not enabled and active in ${name}"
	dump "${name}"
	return 1
}

assert_restored() {
	local name=$1
	docker exec "${name}" systemctl start --no-block udm-iptv-restore.service
	wait_pkg "${name}"
	if docker exec "${name}" journalctl -u udm-iptv-restore.service --no-pager \
		| grep -Fq "warning: no profile at "; then
		report_error "restore emitted a missing-profile warning in ${name}"
		dump "${name}"
		return 1
	fi
	docker exec "${name}" test -e /etc/systemd/system/udm-iptv-restore.service
	docker exec "${name}" systemctl is-enabled --quiet udm-iptv-restore
	wait_active "${name}"
}

assert_version() {
	local name=$1
	local expected=$2
	local actual
	actual=$(docker exec "${name}" dpkg-query -W -f='${Version}' udm-iptv)
	if [[ ${actual} != "${expected}" ]]; then
		report_error "expected udm-iptv ${expected} in ${name}, found ${actual}"
		return 1
	fi
	echo "package version ${actual} in ${name}"
}

assert_removed() {
	local name=$1
	if docker exec "${name}" test -e /usr/bin/udm-iptv; then
		report_error "package still installed in ${name}"
		return 1
	fi
	if docker exec "${name}" systemctl is-enabled --quiet udm-iptv-restore.service 2>/dev/null; then
		report_error "restore service still enabled in ${name}"
		return 1
	fi
}

workflow_emit notice "Testing ${sku}: ${from_image} -> ${to_image}"
echo "from=${from_image}"
echo "to=${to_image}"

group_begin "Prepare package upgrade"
if [[ -n ${OLD_PACKAGE:-} ]]; then
	old_deb=$(readlink -f "${old_deb}")
	if [[ -z ${current_version} || -z ${old_version} ]]; then
		report_error "OLD_PACKAGE requires CURRENT_PACKAGE_VERSION and OLD_PACKAGE_VERSION"
		exit 1
	fi
else
	current_version=$(dpkg-deb -f "${deb}" Version)
	old_version="${current_version}~integration"
	dpkg-deb -R "${deb}" "${old_root}"
	sed -i "s/^Version: .*/Version: ${old_version}/" "${old_root}/DEBIAN/control"
	dpkg-deb --root-owner-group -b "${old_root}" "${old_deb}"
fi
echo "package upgrade=${old_version} -> ${current_version}"
group_end

group_begin "Prepare ARM64 images"
ensure_arm64 "${from_image}"
ensure_arm64 "${to_image}"
group_end

docker volume create "${vol_data}" >/dev/null

group_begin "Install on ${from_image}"
boot "${from_name}" "${from_image}"
wait_systemd "${from_name}"
docker exec \
	-e DEBIAN_FRONTEND=noninteractive \
	-e UDM_IPTV_PACKAGE=/tmp/udm-iptv-old.deb \
	"${from_name}" \
	sh /tmp/install.sh
assert_version "${from_name}" "${old_version}"
docker exec "${from_name}" test -e /data/udm-iptv/udm-iptv.deb
docker exec "${from_name}" test -e /data/udm-iptv/debconf.preseed
docker exec "${from_name}" test -e /data/udm-iptv/udm-iptv.conf
docker exec "${from_name}" test -e /data/udm-iptv/udm-iptv-restore
docker exec "${from_name}" cp /etc/udm-iptv.conf /data/udm-iptv/udm-iptv.conf.installed
docker exec "${from_name}" cp /etc/systemd/system/udm-iptv-restore.service /data/udm-iptv/udm-iptv-restore.service
wait_active "${from_name}"
docker exec "${from_name}" udm-iptv upgrade --package /tmp/udm-iptv.deb
assert_version "${from_name}" "${current_version}"
docker exec "${from_name}" cmp /tmp/udm-iptv.deb /data/udm-iptv/udm-iptv.deb
docker exec "${from_name}" cmp /etc/udm-iptv.conf /data/udm-iptv/udm-iptv.conf.installed
docker exec "${from_name}" systemctl is-enabled --quiet udm-iptv-restore.service
wait_active "${from_name}"
docker stop "${from_name}"
group_end

group_begin "Restore after upgrade to ${to_image}"
boot "${to_name}" "${to_image}"
wait_systemd "${to_name}"
if docker exec "${to_name}" test -e /usr/lib/udm-iptv/udm-iptvd; then
	report_error "package still on /usr after firmware swap"
	exit 1
fi
assert_restored "${to_name}"
docker exec "${to_name}" cmp /etc/udm-iptv.conf /data/udm-iptv/udm-iptv.conf.installed
group_end

group_begin "Restore after rebooting ${to_image}"
docker stop "${to_name}"
docker start "${to_name}"
wait_systemd "${to_name}"
assert_restored "${to_name}"
assert_version "${to_name}" "${current_version}"
docker exec "${to_name}" cmp /etc/udm-iptv.conf /data/udm-iptv/udm-iptv.conf.installed
echo "upgrade and reboot ok"
group_end

group_begin "Remove while retaining saved state"
docker exec "${to_name}" udm-iptv uninstall --keep-data
assert_removed "${to_name}"
docker exec "${to_name}" test -e /etc/udm-iptv.conf
docker exec "${to_name}" test -e /data/udm-iptv/udm-iptv.deb
docker exec "${to_name}" test -e /data/udm-iptv/udm-iptv-restore
docker stop "${to_name}"
docker start "${to_name}"
wait_systemd "${to_name}"
assert_removed "${to_name}"
echo "removed package stayed removed after reboot"
group_end

group_begin "Restore manually, then purge"
docker exec "${to_name}" /data/udm-iptv/udm-iptv-restore
assert_restored "${to_name}"
assert_version "${to_name}" "${current_version}"
docker exec "${to_name}" udm-iptv uninstall
assert_removed "${to_name}"
docker exec "${to_name}" test ! -e /etc/udm-iptv.conf
docker exec "${to_name}" test ! -e /etc/systemd/system/udm-iptv-restore.service
docker exec "${to_name}" test ! -e /data/udm-iptv
docker stop "${to_name}"
docker start "${to_name}"
wait_systemd "${to_name}"
assert_removed "${to_name}"
docker exec "${to_name}" test ! -e /data/udm-iptv
echo "purged package stayed removed after reboot"
group_end
