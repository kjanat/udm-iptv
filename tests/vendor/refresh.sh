#!/usr/bin/env bash
set -euo pipefail

# Refreshes the captured API responses beside this script.
#
#   refresh.sh                    catalog, release index, every release on disk
#   refresh.sh catalog            fw-catalog.json
#   refresh.sh index              ui-releases.json
#   refresh.sh release <slug>...  ui-<family>/<version>.json
#
# community.svc.ui.com drops every beta and RC without the UBIC_AUTH cookie of
# a signed-in community.ui.com session. Point UBIC_COOKIE_FILE at a file
# holding it.

here=$(dirname -- "${BASH_SOURCE[0]}")
for binary in curl git jq; do
	if ! command -v "${binary}" >/dev/null; then
		printf '%s is not installed\n' "${binary}" >&2
		exit 1
	fi
done

if command -v dprint >/dev/null; then
	formatter=(dprint)
elif command -v npx >/dev/null; then
	formatter=(npx -y dprint@latest)
else
	printf 'neither dprint nor npx is installed\n' >&2
	exit 1
fi

root=$(git -C "${here}" rev-parse --show-toplevel)

catalog_url='https://fw-update.ui.com/api/firmware?limit=100000&sort=-created'
index_limit=100
index_search='UniFi OS'

require_cookie() {
	: "${UBIC_COOKIE_FILE:?point it at a file holding the UBIC_AUTH cookie}"
	if [[ ! -r ${UBIC_COOKIE_FILE} ]]; then
		printf '%s is not readable\n' "${UBIC_COOKIE_FILE}" >&2
		return 1
	fi
}

ui_gql() {
	local cookie
	cookie=$(cat -- "${UBIC_COOKIE_FILE}")
	curl -fsS --retry 3 --retry-max-time 45 \
		--connect-timeout 10 --max-time 60 \
		--url https://community.svc.ui.com/ \
		-H 'accept: application/graphql-response+json, application/json' \
		-H 'content-type: application/json' \
		-H 'origin: https://community.ui.com' \
		-H 'referer: https://community.ui.com/' \
		-H 'x-client-id: community-fe' \
		-H 'x-community-domain-identifier: ui' \
		-b "${cookie}" --data @-
}

install_response() {
	local out=$1 response=$2 formatted
	formatted=$("${formatter[@]}" fmt -c "${root}/.dprint.jsonc" --stdin json <<<"${response}")
	mkdir -p -- "${out%/*}"
	printf '%s\n' "${formatted}" >"${out}"
	printf '%s\n' "${out#"${here}/"}"
}

# A rejected query comes back as HTTP 200 carrying an error member.
write_gql() {
	local out=$1 body=$2 response
	response=$(printf '%s' "${body}" | ui_gql)
	if ! jq -e 'has("data") and ((has("errors") or has("error")) | not)' <<<"${response}" >/dev/null; then
		printf 'refusing to write %s, the API answered:\n%s\n' "${out}" "${response}" >&2
		return 1
	fi
	install_response "${out}" "${response}"
}

refresh_catalog() {
	local response
	response=$(curl -fsSL --retry 3 --retry-max-time 45 --max-time 300 "${catalog_url}")
	if ! jq -e '._embedded.firmware | length > 0' <<<"${response}" >/dev/null; then
		printf 'refusing to write the catalog, the response holds no firmware\n' >&2
		return 1
	fi
	install_response "${here}/fw-catalog.json" "${response}"
}

refresh_index() {
	local body
	body=$(jq -n --rawfile query "${here}/releases.graphql" \
		--argjson limit "${index_limit}" \
		--arg search "${index_search}" \
		'{query: $query, variables: {limit: $limit, sortBy: "LATEST", searchTerm: $search}}')
	write_gql "${here}/ui-releases.json" "${body}"
}

refresh_release_id() {
	local id=$1 out=$2 body
	body=$(jq -n --rawfile query "${here}/release.graphql" --arg id "${id}" \
		'{query: $query, variables: {id: $id}}')
	write_gql "${out}" "${body}"
}

# The index carries a slug more than once when a release moved between stages.
refresh_release_slug() {
	local slug=$1 found id version file family
	found=$(jq -er --arg slug "${slug}" \
		'first(.data.releases.items[] | select(.slug == $slug) | "\(.id) \(.version)")' \
		"${here}/ui-releases.json")
	id=${found%% *}
	version=${found#* }
	file=${version//./-}
	if [[ ${slug} != *"-${file}" ]]; then
		printf 'cannot name a file for %s at version %s\n' "${slug}" "${version}" >&2
		return 1
	fi
	family=${slug%"-${file}"}
	family=${family#UniFi-OS-}
	refresh_release_id "${id}" "${here}/ui-${family,,}/${file}.json"
}

refresh_existing() {
	local path id
	for path in "${here}"/ui-*/*.json; do
		[[ -e ${path} ]] || continue
		id=$(jq -er '.data.release.id' "${path}")
		refresh_release_id "${id}" "${path}"
	done
}

case ${1-all} in
	all)
		require_cookie
		refresh_catalog
		refresh_index
		refresh_existing
		;;
	catalog)
		refresh_catalog
		;;
	index)
		require_cookie
		refresh_index
		;;
	release)
		require_cookie
		shift
		if (($# == 0)); then
			printf 'release wants a slug from ui-releases.json\n' >&2
			exit 1
		fi
		for slug in "$@"; do
			refresh_release_slug "${slug}"
		done
		;;
	*)
		printf 'usage: %s [all|catalog|index|release <slug>...]\n' "${0##*/}" >&2
		exit 1
		;;
esac
