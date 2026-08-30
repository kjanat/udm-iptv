#!/bin/sh
set -eu

here=$(dirname -- "$0")
here=$(cd -- "${here}" && pwd)
catalog="${here}/firmware.tsv"
image="${UNIFI_OS_IMAGE:-ghcr.io/${GITHUB_REPOSITORY_OWNER:-fabianishere}/unifi-os}"
package="${image#ghcr.io/}"
user_package=${package#*/}
sku="${1:-}"

if [ -z "${sku}" ]; then
	echo "usage: $0 <sku|all>" >&2
	exit 1
fi

found=0
while IFS="$(printf '\t')" read -r key board version _; do
	[ -n "${key}" ] || continue
	if [ "${sku}" = "all" ] || [ "${sku}" = "${key}" ]; then
		docker push "${image}:${key}-${version}"
		docker push "${image}:${key}-${board}-${version}"
		found=1
	fi
done <"${catalog}"

if [ "${found}" -eq 0 ]; then
	echo "error: unknown sku ${sku}" >&2
	exit 1
fi

visibility=$(gh api "/user/packages/container/${user_package}" --jq .visibility)
echo "visibility=${visibility}"
if [ "${visibility}" != "private" ]; then
	gh api --method POST "/user/packages/container/${user_package}/visibility" -f visibility=private
	visibility=$(gh api "/user/packages/container/${user_package}" --jq .visibility)
	echo "visibility=${visibility}"
fi
if [ "${visibility}" != "private" ]; then
	echo "error: package ${image} is ${visibility}, deleting it" >&2
	gh api --method DELETE "/user/packages/container/${user_package}"
	exit 1
fi
