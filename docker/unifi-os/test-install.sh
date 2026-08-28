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

cleanup() {
	docker rm -f "${from_name}" "${to_name}" >/dev/null 2>&1 || true
	docker volume rm "${vol_data}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

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
		-e DEBIAN_FRONTEND=noninteractive \
		-e UDM_IPTV_PACKAGE=/tmp/udm-iptv.deb \
		"${image}" \
		/harness.sh
}

wait_systemd() {
	name=$1
	n=0
	while [ "${n}" -lt 60 ]; do
		if [ "$(docker inspect -f '{{.State.Running}}' "${name}" 2>/dev/null || echo false)" = "true" ] \
			&& docker exec "${name}" test -d /run/systemd/system; then
			return 0
		fi
		sleep 2
		n=$((n + 2))
	done
	echo "error: systemd did not start in ${name}" >&2
	docker inspect -f '{{.State.Status}} {{.State.Error}}' "${name}" >&2 || true
	docker logs "${name}" >&2 || true
	return 1
}

wait_active() {
	name=$1
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
	echo "error: udm-iptv is not enabled and active in ${name}" >&2
	docker exec "${name}" systemctl status udm-iptv-restore.service udm-iptv.service >&2 || true
	docker exec "${name}" journalctl -u udm-iptv-restore -u udm-iptv --no-pager >&2 || true
	docker logs "${name}" >&2 || true
	return 1
}

assert_restored() {
	name=$1
	docker exec "${name}" test -e /usr/bin/udm-iptv
	docker exec "${name}" test -e /usr/lib/udm-iptv/udm-iptvd
	docker exec "${name}" test -e /etc/systemd/system/udm-iptv-restore.service
	docker exec "${name}" systemctl is-enabled --quiet udm-iptv-restore
	wait_active "${name}"
}

echo "from=${from_image}"
echo "to=${to_image}"

ensure_arm64 "${from_image}"
ensure_arm64 "${to_image}"

docker volume create "${vol_data}" >/dev/null

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
echo "starting udm-iptv on ${from_name}"
timeout 30 docker exec "${from_name}" systemctl start udm-iptv || true
wait_active "${from_name}"
docker stop "${from_name}"

echo "booting ${to_image}"
boot "${to_name}" "${to_image}"
wait_systemd "${to_name}"
echo "to systemd up"
if docker exec "${to_name}" test -e /usr/lib/udm-iptv/udm-iptvd; then
	echo "error: package still on /usr after firmware swap" >&2
	exit 1
fi
assert_restored "${to_name}"
docker exec "${to_name}" cmp /etc/udm-iptv.conf /data/udm-iptv/udm-iptv.conf.installed

docker stop "${to_name}"
docker start "${to_name}"
wait_systemd "${to_name}"
assert_restored "${to_name}"
docker exec "${to_name}" cmp /etc/udm-iptv.conf /data/udm-iptv/udm-iptv.conf.installed
echo "upgrade and reboot ok"
