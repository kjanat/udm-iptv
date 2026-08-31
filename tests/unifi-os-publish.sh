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

image=ghcr.io/example/unifi-os
expected_tags=(
	"${image}":udmpro-{5.1.26,5.1.31,latest}
	"${image}":UDMPRO-{5.1.26,5.1.31,latest}
	"${image}:latest"
)
expected_tags_file="${work}/expected-tags"
pushed_tags_file="${work}/pushed-tags"
printf '%s\n' "${expected_tags[@]}" >"${expected_tags_file}"
awk '$1 == "docker" && $2 == "push" { print $3 }' "${log}" >"${pushed_tags_file}"
LC_ALL=C sort -o "${expected_tags_file}" "${expected_tags_file}"
LC_ALL=C sort -o "${pushed_tags_file}" "${pushed_tags_file}"
diff -u "${expected_tags_file}" "${pushed_tags_file}"

grep -Fqx 'gh api /user/packages/container/unifi-os --jq .visibility' "${log}"

if grep -Eq -- '--method (POST|PUT|PATCH|DELETE)' "${log}"; then
	echo "error: publishing attempted to mutate the package" >&2
	exit 1
fi
