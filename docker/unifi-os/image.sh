#!/bin/sh
set -eu

here=$(dirname -- "$0")
here=$(cd -- "$here" && pwd)
catalog="${here}/firmware.tsv"
image="${UNIFI_OS_IMAGE:-ghcr.io/kjanat/unifi-os}"
wanted="${1:-all}"
found=0

while IFS="$(printf '\t')" read -r key _ version _; do
	[ -n "${key}" ] || continue
	if [ "${wanted}" = "all" ] || [ "${wanted}" = "${key}" ]; then
		echo "${image}:${key}-${version}"
		found=1
	fi
done <"${catalog}"

if [ "${found}" -eq 0 ]; then
	echo "error: unknown sku ${wanted}" >&2
	exit 1
fi
