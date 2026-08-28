#!/bin/sh
set -eu

here=$(dirname -- "$0")
here=$(cd -- "$here" && pwd)
catalog="${here}/firmware.tsv"
image="${UNIFI_OS_IMAGE:-ghcr.io/kjanat/unifi-os}"
sku="${1:-}"

if [ -z "${sku}" ] || [ "${sku}" = "all" ]; then
	echo "usage: $0 <sku>" >&2
	exit 1
fi

versions=$(
	while IFS="$(printf '\t')" read -r key _ version _; do
		[ "${key}" = "${sku}" ] || continue
		printf '%s\n' "${version}"
	done <"${catalog}" | sort -V
)

if [ -z "${versions}" ]; then
	echo "error: unknown sku ${sku}" >&2
	exit 1
fi

from_version=
to_version=
while IFS= read -r version; do
	from_version=${to_version}
	to_version=${version}
done <<END
${versions}
END

if [ -z "${from_version}" ] || [ -z "${to_version}" ]; then
	echo "error: sku ${sku} needs two firmware versions to test upgrade" >&2
	exit 1
fi

printf 'from_image=%s\n' "${image}:${sku}-${from_version}"
printf 'to_image=%s\n' "${image}:${sku}-${to_version}"
