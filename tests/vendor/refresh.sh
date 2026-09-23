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
# a signed-in community.ui.com session. Put it in UBIC_AUTH, or name a file
# holding it in UBIC_AUTH_FILE. The catalog needs neither.

here=$(dirname -- "${BASH_SOURCE[0]}")

say() {
	printf '[%s] %s\n' "$1" "$2" >&2
}

fail() {
	printf 'ERROR: %s\n' "$1" >&2
}

for binary in curl git jq; do
	if ! command -v "${binary}" >/dev/null; then
		fail "${binary} is not installed"
		exit 1
	fi
done

if command -v dprint >/dev/null; then
	formatter=(dprint)
elif command -v npx >/dev/null; then
	formatter=(npx -y dprint@latest)
else
	fail 'neither dprint nor npx is installed'
	exit 1
fi

root=$(git -C "${here}" rev-parse --show-toplevel)

catalog_url='https://fw-update.ui.com/api/firmware?limit=100000&sort=-created'
index_limit=100
index_search='UniFi OS'

cookie=
cookie_from=nothing
if [[ -n ${UBIC_AUTH-} ]]; then
	cookie=UBIC_AUTH=${UBIC_AUTH#UBIC_AUTH=}
	cookie_from=UBIC_AUTH
elif [[ -n ${UBIC_AUTH_FILE-} ]]; then
	cookie=$(cat -- "${UBIC_AUTH_FILE}")
	cookie=UBIC_AUTH=${cookie#UBIC_AUTH=}
	cookie_from=${UBIC_AUTH_FILE}
fi

require_cookie() {
	if [[ -n ${cookie} ]]; then
		return 0
	fi
	fail 'no cookie: set UBIC_AUTH, or UBIC_AUTH_FILE to a file holding it'
	return 1
}

ui_gql() {
	curl -fsS --retry 3 --retry-max-time 45 \
		--connect-timeout 10 --max-time 60 \
		--url https://community.svc.ui.com/ \
		--header 'accept: application/graphql-response+json, application/json' \
		--header 'content-type: application/json' \
		--header 'origin: https://community.ui.com' \
		--header 'referer: https://community.ui.com/' \
		--header 'x-client-id: community-fe' \
		--header 'x-community-domain-identifier: ui' \
		--cookie "${cookie}" --data @- \
		--write-out '%{stderr}[http] %{http_code} in %{time_total}s, %{size_download} bytes\n'
}

describe() {
	jq -r --arg kind "$1" -f "${here}/describe.jq"
}

shape() {
	jq --arg kind "$1" -f "${here}/shape.jq"
}

install_response() {
	local tag=$1 out=$2 response=$3 name shaped formatted summary after before=''
	name=${out#"${here}/"}
	say "${tag}" "Destination: ${name}"
	shaped=$(shape "${tag}" <<<"${response}")
	formatted=$(cd -- "${root}" && "${formatter[@]}" fmt --stdin json <<<"${shaped}")
	summary=$(describe "${tag}" <<<"${response}")
	mkdir -p -- "${out%/*}"
	if [[ -f ${out} ]]; then
		before=$(cksum <"${out}")
	fi
	printf '%s\n' "${formatted}" >"${out}"
	after=$(cksum <"${out}")
	if [[ ${after} == "${before}" ]]; then
		say "${tag}" "Unchanged, ${summary}"
	else
		say "${tag}" "Written, ${summary}"
	fi
}

refuse() {
	local tag=$1 out=$2 response=$3 problem=$4 kind=${5:-$1} summary
	summary=$(describe "${kind}" <<<"${response}")
	fail "[${tag}] ${out#"${here}/"} not written: ${problem}"
	say "${tag}" "Response held ${summary}"
}

# A rejected query comes back as HTTP 200 carrying an error member, and a
# session without release-testing access comes back complete but abridged.
write_gql() {
	local tag=$1 out=$2 body=$3 sound=$4 problem=$5 response
	response=$(printf '%s' "${body}" | ui_gql)
	if ! jq -e 'has("data") and ((has("errors") or has("error")) | not)' <<<"${response}" >/dev/null; then
		refuse "${tag}" "${out}" "${response}" 'the API rejected the query' failure
		return 1
	fi
	if ! jq -e "${sound}" <<<"${response}" >/dev/null; then
		refuse "${tag}" "${out}" "${response}" "${problem}"
		return 1
	fi
	install_response "${tag}" "${out}" "${response}"
}

refresh_catalog() {
	local response
	say catalog "Downloading ${catalog_url}"
	response=$(curl -fL --progress-bar --retry 3 --retry-max-time 45 --max-time 300 "${catalog_url}")
	if ! jq -e '._embedded.firmware | length > 0' <<<"${response}" >/dev/null; then
		refuse catalog "${here}/fw-catalog.json" "${response}" 'no firmware in the response'
		return 1
	fi
	install_response catalog "${here}/fw-catalog.json" "${response}"
}

refresh_index() {
	local body
	say index "Signing in with the cookie from ${cookie_from}"
	say index "Querying releases(limit: ${index_limit}, sortBy: LATEST, searchTerm: \"${index_search}\")"
	body=$(jq -n --rawfile query "${here}/releases.graphql" \
		--argjson limit "${index_limit}" \
		--arg search "${index_search}" \
		'{query: $query, variables: {limit: $limit, sortBy: "LATEST", searchTerm: $search}}')
	write_gql index "${here}/ui-releases.json" "${body}" \
		'[.data.releases.items[].stage] | index("T") != null' \
		'no T stage, this cookie cannot see release testing'
}

refresh_release_id() {
	local id=$1 out=$2 body
	say release "Querying release(id: ${id})"
	body=$(jq -n --rawfile query "${here}/release.graphql" --arg id "${id}" \
		'{query: $query, variables: {id: $id}}')
	write_gql release "${out}" "${body}" '.data.release.links | length > 0' \
		'no download links'
}

# A slug appears under two stages when a release moved between them, and the
# newest one sorts first.
refresh_release_slug() {
	local slug=$1 found id version file family
	if ! found=$(jq -er --arg slug "${slug}" -f "${here}/lookup.jq" \
		"${here}/ui-releases.json"); then
		fail "[release] ${slug} is not in ui-releases.json"
		return 1
	fi
	id=${found%% *}
	version=${found#* }
	file=${version//./-}
	if [[ ${slug} != *"-${file}" ]]; then
		fail "[release] cannot name a file for ${slug} at version ${version}"
		return 1
	fi
	family=${slug%"-${file}"}
	family=${family#UniFi-OS-}
	say release "${slug} is ${id}"
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
		refresh_catalog
		if [[ -n ${cookie} ]]; then
			refresh_index
			refresh_existing
		else
			say index 'Skipping: no cookie, so the index and releases stay as they are'
		fi
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
			fail '[release] wants a slug from ui-releases.json'
			exit 1
		fi
		for slug in "$@"; do
			refresh_release_slug "${slug}"
		done
		;;
	*)
		fail "usage: ${0##*/} [all|catalog|index|release <slug>...]"
		exit 1
		;;
esac
