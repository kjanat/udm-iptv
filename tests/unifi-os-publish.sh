#!/usr/bin/env bash
set -euo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "${work}"' EXIT
mkdir "${work}/bin"
log="${work}/commands"
visibility="${work}/visibility"
echo private >"${visibility}"

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
if [[ " $* " == *' --method POST '* ]]; then
	echo public >"${VISIBILITY_FILE}"
else
	cat "${VISIBILITY_FILE}"
fi
EOF
chmod +x "${work}/bin/docker" "${work}/bin/gh"

PATH="${work}/bin:${PATH}" \
	COMMAND_LOG="${log}" \
	VISIBILITY_FILE="${visibility}" \
	MODEL=udmpro \
	UNIFI_OS_IMAGE=ghcr.io/example/unifi-os \
	PUBLISH_LATEST=true \
	UNIFI_OS_FIRMWARES='[
		{"board":"UDMPRO","version":"5.1.26"},
		{"board":"UDMPRO","version":"5.1.31"}
	]' \
	"${repo}/.github/actions/unifi-os-publish/publish.bash"

for command in \
	'docker tag ghcr.io/example/unifi-os:udmpro-5.1.31 ghcr.io/example/unifi-os:udmpro-latest' \
	'docker push ghcr.io/example/unifi-os:udmpro-latest' \
	'docker tag ghcr.io/example/unifi-os:udmpro-5.1.31 ghcr.io/example/unifi-os:udmpro-UDMPRO-latest' \
	'docker push ghcr.io/example/unifi-os:udmpro-UDMPRO-latest' \
	'docker tag ghcr.io/example/unifi-os:udmpro-5.1.31 ghcr.io/example/unifi-os:latest' \
	'docker push ghcr.io/example/unifi-os:latest' \
	'gh api --method POST /user/packages/container/unifi-os/visibility -f visibility=public'; do
	grep -Fqx "${command}" "${log}"
done

if grep -Fq -- '--method DELETE' "${log}"; then
	echo "error: publishing attempted to delete the package" >&2
	exit 1
fi
