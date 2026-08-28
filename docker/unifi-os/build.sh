#!/bin/sh
set -eu

here=$(dirname -- "$0")
here=$(cd -- "${here}" && pwd)
catalog="${here}/firmware.tsv"
cache="${UNIFI_OS_CACHE:-${HOME}/.cache/unifi-os}"
image="${UNIFI_OS_IMAGE:-ghcr.io/kjanat/unifi-os}"
sku="${1:-}"

if [ -z "${sku}" ]; then
	echo "usage: $0 <sku|all>" >&2
	echo "skus:" >&2
	"${here}/skus.sh" >&2
	exit 1
fi

build_one() {
	key=$1
	board=$2
	version=$3
	url=$4
	tag="${key}-${version}"
	fwdir="${cache}/firmware/${tag}"
	root="${cache}/rootfs/${tag}"
	bin="${fwdir}/firmware.bin"

	mkdir -p "${fwdir}" "${cache}/rootfs"
	if [ ! -s "${bin}" ]; then
		echo "Downloading ${url}"
		curl -fL --retry 3 -A "curl/8.5.0" -o "${bin}.partial" "${url}"
		mv "${bin}.partial" "${bin}"
	fi

	python3 "${here}/extract.py" "${bin}" "${fwdir}"
	rm -rf "${root}"
	unsquashfs -d "${root}" "${fwdir}/rootfs.squashfs"

	tar --numeric-owner -C "${root}" -cf - . | docker import \
		--platform linux/arm64 \
		--change "CMD [\"/bin/bash\"]" \
		--change "ENV DEBIAN_FRONTEND=noninteractive" \
		- "${image}:${tag}"

	docker tag "${image}:${tag}" "${image}:${key}-${board}-${version}"
	echo "${image}:${tag}"
}

found=0
while IFS="$(printf '\t')" read -r key board version url; do
	[ -n "${key}" ] || continue
	if [ "${sku}" = "all" ] || [ "${sku}" = "${key}" ]; then
		build_one "${key}" "${board}" "${version}" "${url}"
		found=1
	fi
done <"${catalog}"

if [ "${found}" -eq 0 ]; then
	echo "error: unknown sku ${sku}" >&2
	exit 1
fi
