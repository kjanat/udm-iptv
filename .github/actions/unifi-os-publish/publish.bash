#!/usr/bin/env bash
set -euo pipefail

: "${MODEL:?MODEL is required}"
: "${UNIFI_OS_FIRMWARES:?UNIFI_OS_FIRMWARES is required}"
: "${UNIFI_OS_IMAGE:?UNIFI_OS_IMAGE is required}"
publish_latest=${PUBLISH_LATEST:-false}
case ${publish_latest} in
	true | false) ;;
	*)
		echo "error: PUBLISH_LATEST must be true or false" >&2
		exit 2
		;;
esac

firmwares=$(jq -er '.[] | [.board, .version] | @tsv' \
	<<<"${UNIFI_OS_FIRMWARES}")
latest_board=
latest_version=
while IFS=$'\t' read -r board version; do
	docker push "${UNIFI_OS_IMAGE}:${MODEL}-${version}"
	docker push "${UNIFI_OS_IMAGE}:${MODEL}-${board}-${version}"
	latest_board=${board}
	latest_version=${version}
done <<<"${firmwares}"

if [[ -z ${latest_board} || -z ${latest_version} ]]; then
	echo "error: firmware list is empty" >&2
	exit 1
fi

source_image="${UNIFI_OS_IMAGE}:${MODEL}-${latest_version}"
for alias in "${MODEL}-latest" "${MODEL}-${latest_board}-latest"; do
	docker tag "${source_image}" "${UNIFI_OS_IMAGE}:${alias}"
	docker push "${UNIFI_OS_IMAGE}:${alias}"
done
if [[ ${publish_latest} == true ]]; then
	docker tag "${source_image}" "${UNIFI_OS_IMAGE}:latest"
	docker push "${UNIFI_OS_IMAGE}:latest"
fi

package=${UNIFI_OS_IMAGE#ghcr.io/}
user_package=${package#*/}
visibility=$(gh api "/user/packages/container/${user_package}" --jq .visibility)
echo "visibility=${visibility}"
if [[ ${visibility} != public ]]; then
	echo "error: package ${UNIFI_OS_IMAGE} is ${visibility}; set its visibility to public in GitHub Packages" >&2
	exit 1
fi
