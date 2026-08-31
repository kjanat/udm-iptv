#!/usr/bin/env bash
set -euo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "${work}"' EXIT
mkdir "${work}/bin"
log="${work}/commands"

cat >"${work}/bin/docker" <<'EOF'
#!/usr/bin/env bash
printf 'docker' >>"${COMMAND_LOG}"
printf ' %q' "$@" >>"${COMMAND_LOG}"
printf '\n' >>"${COMMAND_LOG}"
EOF
cat >"${work}/bin/gh" <<'EOF'
#!/usr/bin/env bash
printf 'gh' >>"${COMMAND_LOG}"
printf ' %q' "$@" >>"${COMMAND_LOG}"
printf '\n' >>"${COMMAND_LOG}"
echo "${GH_VISIBILITY}"
EOF
chmod +x "${work}/bin/docker" "${work}/bin/gh"

PATH="${work}/bin:${PATH}" \
	COMMAND_LOG="${log}" \
	GH_VISIBILITY=public \
	MODEL=udmpro \
	UNIFI_OS_IMAGE=ghcr.io/example/unifi-os \
	PUBLISH_LATEST=true \
	UNIFI_OS_FIRMWARES='[
		{"board":"UDMPRO","version":"5.1.26"},
		{"board":"UDMPRO","version":"5.1.31"}
	]' \
	"${repo}/.github/actions/unifi-os-publish/publish.bash"

for command in \
	'docker tag ghcr.io/example/unifi-os:udmpro-5.1.26 ghcr.io/example/unifi-os:UDMPRO-5.1.26' \
	'docker push ghcr.io/example/unifi-os:UDMPRO-5.1.26' \
	'docker tag ghcr.io/example/unifi-os:udmpro-5.1.31 ghcr.io/example/unifi-os:UDMPRO-5.1.31' \
	'docker push ghcr.io/example/unifi-os:UDMPRO-5.1.31' \
	'docker tag ghcr.io/example/unifi-os:udmpro-5.1.31 ghcr.io/example/unifi-os:udmpro-latest' \
	'docker push ghcr.io/example/unifi-os:udmpro-latest' \
	'docker tag ghcr.io/example/unifi-os:udmpro-5.1.31 ghcr.io/example/unifi-os:UDMPRO-latest' \
	'docker push ghcr.io/example/unifi-os:UDMPRO-latest' \
	'docker tag ghcr.io/example/unifi-os:udmpro-5.1.31 ghcr.io/example/unifi-os:latest' \
	'docker push ghcr.io/example/unifi-os:latest' \
	'gh api /user/packages/container/unifi-os --jq .visibility'; do
	grep -Fqx "${command}" "${log}"
done

if grep -Eq -- '--method (POST|PUT|PATCH|DELETE)' "${log}"; then
	echo "error: publishing attempted to mutate the package" >&2
	exit 1
fi

if grep -Eq -- 'udmpro-UDMPRO-' "${log}"; then
	echo "error: publishing combined model-board tags" >&2
	exit 1
fi
