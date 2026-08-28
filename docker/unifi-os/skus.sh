#!/bin/sh
set -eu

here=$(dirname -- "$0")
here=$(cd -- "${here}" && pwd)
catalog="${here}/firmware.tsv"
wanted="${1:-all}"
keys=$(
	while IFS="$(printf '\t')" read -r key _ _ _; do
		[ -n "${key}" ] || continue
		if [ "${wanted}" = "all" ] || [ "${wanted}" = "${key}" ]; then
			printf '%s\n' "${key}"
		fi
	done <"${catalog}" | sort -u
)

if [ -z "${keys}" ]; then
	echo "error: unknown sku ${wanted}" >&2
	exit 1
fi

printf '%s\n' "${keys}"
