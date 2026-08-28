#!/bin/sh
set -eu

here=$(dirname -- "$0")
here=$(cd -- "$here" && pwd)
repo=$(cd -- "${here}/../.." && pwd)
sku="${1:-}"
deb="${2:-}"

if [ -z "${sku}" ] || [ -z "${deb}" ]; then
	echo "usage: $0 <sku> <deb>" >&2
	exit 1
fi

if [ -n "${FROM_IMAGE:-}" ] && [ -n "${TO_IMAGE:-}" ]; then
	from_image=${FROM_IMAGE}
	to_image=${TO_IMAGE}
else
	eval "$("${here}/upgrade-pair.sh" "${sku}")"
fi
deb=$(readlink -f "${deb}")
id="udm-iptv-${sku}-$$"
vol_data="${id}-data"
from_name="${id}-from"
to_name="${id}-to"
group_open=0

workflow_escape() {
	printf '%s' "$1" | awk '
		BEGIN { first = 1 }
		{
			gsub(/%/, "%25")
			gsub(/\r/, "%0D")
			if (!first) printf "%%0A"
			printf "%s", $0
			first = 0
		}
	'
}

workflow_emit() {
	command=$1
	message=$2
	if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
		printf '::%s::%s\n' "${command}" "$(workflow_escape "${message}")"
	fi
}

workflow_error() {
	message=$1
	if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
		printf '::error file=docker/unifi-os/test-install.sh,title=UniFi OS integration test::%s\n' \
			"$(workflow_escape "${message}")"
	fi
}

report_error() {
	message=$1
	workflow_error "${message}"
	echo "error: ${message}" >&2
}

group_end() {
	if [ "${group_open}" -eq 1 ]; then
		workflow_emit endgroup ""
		group_open=0
	fi
}

group_begin() {
	title=$1
	group_end
	if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
		workflow_emit group "${title}"
	else
		printf '\n==> %s\n' "${title}"
	fi
	group_open=1
}

cleanup() {
	status=$?
	trap - EXIT
	group_end
	docker rm -f "${from_name}" "${to_name}" >/dev/null 2>&1 || true
	docker volume rm "${vol_data}" >/dev/null 2>&1 || true
	exit "${status}"
}
trap cleanup EXIT

dump() {
	name=$1
	group_begin "Diagnostics: ${name}"
	echo "container status=$(docker inspect -f '{{.State.Status}}' "${name}" 2>/dev/null || echo gone)" >&2
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
	image=$1
	if docker run --rm --platform linux/arm64 "${image}" uname -m; then
		return 0
	fi
	docker run --privileged --rm tonistiigi/binfmt --install arm64
	docker run --rm --platform linux/arm64 "${image}" uname -m
}

boot() {
	name=$1
	image=$2
	docker rm -f "${name}" >/dev/null 2>&1 || true
	docker run -d --name "${name}" --platform linux/arm64 --privileged --cgroupns=host \
		--stop-signal SIGRTMIN+3 \
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw \
		--tmpfs /run:exec --tmpfs /run/lock --tmpfs /tmp:exec \
		-v "${vol_data}:/data" \
		-v "${deb}:/tmp/udm-iptv.deb:ro" \
		-v "${repo}/install.sh:/tmp/install.sh:ro" \
		-v "${here}/harness.sh:/harness.sh:ro" \
		-v "${here}/udm-iptv-test.target:/etc/systemd/system/udm-iptv-test.target:ro" \
		-e DEBIAN_FRONTEND=noninteractive \
		-e UDM_IPTV_PACKAGE=/tmp/udm-iptv.deb \
		"${image}" \
		/harness.sh
}

wait_systemd() {
	name=$1
	n=0
	while [ "${n}" -lt 60 ]; do
		status=$(docker inspect -f '{{.State.Status}}' "${name}" 2>/dev/null || echo gone)
		if [ "${status}" = "exited" ] || [ "${status}" = "dead" ] || [ "${status}" = "gone" ]; then
			report_error "${name} ${status} while waiting for the test target"
			dump "${name}"
			return 1
		fi
		if [ "${status}" = "running" ] \
			&& docker exec "${name}" test -S /run/systemd/private \
			&& docker exec "${name}" systemctl is-active --quiet udm-iptv-test.target; then
			echo "test target active in ${name}"
			return 0
		fi
		if [ $((n % 10)) -eq 0 ]; then
			workflow_emit debug "Waiting for the test target in ${name}: container=${status}, elapsed=${n}s"
		fi
		sleep 2
		n=$((n + 2))
	done
	report_error "test target did not become active in ${name}"
	dump "${name}"
	return 1
}

wait_pkg() {
	name=$1
	n=0
	while [ "${n}" -lt 180 ]; do
		if docker exec "${name}" test -e /usr/bin/udm-iptv \
			&& docker exec "${name}" test -e /usr/lib/udm-iptv/udm-iptvd; then
			echo "package present in ${name}"
			return 0
		fi
		sleep 2
		n=$((n + 2))
	done
	report_error "restore did not install the package in ${name}"
	dump "${name}"
	return 1
}

wait_active() {
	name=$1
	timeout 30 docker exec "${name}" systemctl start udm-iptv || true
	n=0
	while [ "${n}" -lt 180 ]; do
		if docker exec "${name}" systemctl is-enabled --quiet udm-iptv \
			&& docker exec "${name}" systemctl is-active --quiet udm-iptv; then
			docker exec "${name}" systemctl is-enabled udm-iptv
			docker exec "${name}" systemctl is-active udm-iptv
			return 0
		fi
		sleep 2
		n=$((n + 2))
	done
	report_error "udm-iptv is not enabled and active in ${name}"
	dump "${name}"
	return 1
}

assert_restored() {
	name=$1
	timeout 120 docker exec "${name}" systemctl start udm-iptv-restore.service || true
	wait_pkg "${name}"
	docker exec "${name}" test -e /etc/systemd/system/udm-iptv-restore.service
	docker exec "${name}" systemctl is-enabled --quiet udm-iptv-restore
	wait_active "${name}"
}

workflow_emit notice "Testing ${sku}: ${from_image} -> ${to_image}"
echo "from=${from_image}"
echo "to=${to_image}"

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
	-e UDM_IPTV_PACKAGE=/tmp/udm-iptv.deb \
	"${from_name}" \
	sh /tmp/install.sh
docker exec "${from_name}" test -e /data/udm-iptv/udm-iptv.deb
docker exec "${from_name}" test -e /data/udm-iptv/debconf.preseed
docker exec "${from_name}" test -e /data/udm-iptv/udm-iptv.conf
docker exec "${from_name}" test -e /data/udm-iptv/udm-iptv-restore
docker exec "${from_name}" cp /etc/udm-iptv.conf /data/udm-iptv/udm-iptv.conf.installed
docker exec "${from_name}" cp /etc/systemd/system/udm-iptv-restore.service /data/udm-iptv/udm-iptv-restore.service
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
docker exec "${to_name}" cmp /etc/udm-iptv.conf /data/udm-iptv/udm-iptv.conf.installed
echo "upgrade and reboot ok"
group_end
