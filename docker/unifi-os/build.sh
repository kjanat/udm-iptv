#!/bin/sh
set -eu

here=$(dirname -- "$0")
here=$(cd -- "${here}" && pwd)
catalog="${here}/firmware.tsv"
cache="${UNIFI_OS_CACHE:-${HOME}/.cache/unifi-os}"
image="${UNIFI_OS_IMAGE:-ghcr.io/kjanat/unifi-os}"
sku="${1:-}"

have_root() {
	if [ "$(id -u)" -eq 0 ]; then
		return 0
	fi
	command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1
}

remove_root() {
	remove_path=$1
	if have_root; then
		if [ "$(id -u)" -eq 0 ]; then
			rm -rf "${remove_path}"
		else
			sudo -n rm -rf "${remove_path}"
		fi
	elif [ -d "${remove_path}" ]; then
		docker run --rm -v "${remove_path}:/rootfs" ubuntu:24.04 \
			find /rootfs -mindepth 1 -delete
		rmdir "${remove_path}"
	fi
}

extract_rootfs() {
	extract_source=$1
	extract_target=$2
	if have_root; then
		if [ "$(id -u)" -eq 0 ]; then
			unsquashfs -xattrs -d "${extract_target}" "${extract_source}"
		else
			sudo -n unsquashfs -xattrs -d "${extract_target}" "${extract_source}"
		fi
	else
		mkdir -p "${extract_target}"
		docker run --rm \
			-v "${extract_source}:/input/rootfs.squashfs:ro" \
			-v "${extract_target}:/rootfs" \
			ubuntu:24.04 sh -eu -c \
			'apt-get update -qq
			apt-get install -qq -y squashfs-tools
			unsquashfs -xattrs -d /rootfs /input/rootfs.squashfs'
	fi
}

archive_rootfs() {
	archive_source=$1
	if have_root; then
		if [ "$(id -u)" -eq 0 ]; then
			tar --xattrs --xattrs-include='*' --numeric-owner \
				-C "${archive_source}" -cf - .
		else
			sudo -n tar --xattrs --xattrs-include='*' --numeric-owner \
				-C "${archive_source}" -cf - .
		fi
	else
		docker run --rm -v "${archive_source}:/rootfs:ro" ubuntu:24.04 \
			tar --xattrs --xattrs-include='*' --numeric-owner -C /rootfs -cf - .
	fi
}

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
	else
		echo "Using cached ${bin}"
	fi

	python3 "${here}/extract.py" "${bin}" "${fwdir}"
	remove_root "${root}"
	extract_rootfs "${fwdir}/rootfs.squashfs" "${root}"

	archive_rootfs "${root}" | docker import \
		--platform linux/arm64 \
		--change "CMD [\"/bin/bash\"]" \
		--change "ENV DEBIAN_FRONTEND=noninteractive" \
		--change "LABEL org.opencontainers.image.description=\"Extracted UniFi OS root filesystem for package lifecycle tests; not a bootable firmware image\"" \
		- "${image}:${tag}"

	docker tag "${image}:${tag}" "${image}:${key}-${board}-${version}"
	remove_root "${root}"
	rm -f "${fwdir}/rootfs.squashfs" "${fwdir}/uboot.bin" "${fwdir}/kernel.bin"
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
