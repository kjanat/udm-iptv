#!/usr/bin/env bash
# Run the Go registration regression against an already-built multicast image.
set -euo pipefail

implementation="${1:-upstream}"
case "${implementation}" in
	upstream | patched | firmware) ;;
	*)
		echo "Expected upstream, patched, or firmware" >&2
		exit 2
		;;
esac

cd "$(dirname "${BASH_SOURCE[0]}")/../.."
image="udm-iptv-multicast:${implementation}"
case "$(uname -m)" in
	aarch64 | arm64) host_arch=arm64 ;;
	x86_64 | amd64) host_arch=amd64 ;;
	*)
		echo "Unsupported native architecture" >&2
		exit 2
		;;
esac
image_arch="$(docker image inspect --format '{{.Architecture}}' "${image}")"
if [[ "${host_arch}" != "${image_arch}" ]]; then
	echo "Native execution required: host ${host_arch}, image ${image_arch}" >&2
	exit 2
fi

mkdir -p tests/multicast/artifacts
evidence="$(mktemp -d "${PWD}/tests/multicast/artifacts/registration-${implementation}-XXXXXX")"
echo "Registration evidence: ${evidence}"
container="$(docker create --network none "${image}")"
trap 'docker rm "${container}" >/dev/null' EXIT
# The container is never started; the Go test owns private network/mount namespaces.
docker cp "${container}:/opt/proxy/improxy" "${evidence}/improxy"
docker cp "${container}:/opt/proxy/revision" "${evidence}/revision"
docker image inspect "${image}" >"${evidence}/image.json"
uname -a >"${evidence}/kernel.txt"
sha256sum "${evidence}/improxy" >"${evidence}/improxy.sha256"
go test -c -tags integration -o "${evidence}/service.test" ./internal/service

test_command=(env "UDM_IPTV_TEST_IMPROXY=${evidence}/improxy" "${evidence}/service.test"
	-test.run '^TestProxyRegistrationRejectsPartialStartup$' -test.v -test.timeout 45s)
registration_uid="$(id -u)"
if [[ "${registration_uid}" == 0 ]]; then
	"${test_command[@]}" 2>&1 | tee "${evidence}/test.log"
else
	sudo -n "${test_command[@]}" 2>&1 | tee "${evidence}/test.log"
fi
