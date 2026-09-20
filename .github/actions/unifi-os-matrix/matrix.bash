#!/usr/bin/env bash
set -euo pipefail

# Required:
# SKU             one console, named as the catalog's platform in lower case
#                 (udmpro, udmprose, udr7, uxmax, ...), or all
# UNIFI_OS_IMAGE  container repository the image tags are built under
#                 (ghcr.io/kjanat/unifi-os)
# Optional:
# CHANNELS        catalog channels to draw from (default: release beta-public)
# CUTOFF          ignore firmware published after this RFC 3339 instant
# PINNED          builds the catalog does not index, paired with the newest
#                 release for their console (default: pinned.json beside this
#                 script, skipped when absent or empty)
: "${SKU:?SKU is required}"
: "${UNIFI_OS_IMAGE:?UNIFI_OS_IMAGE is required}"

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

# release is required. Every other channel contributes what it has, so a
# channel Ubiquiti has published nothing on for these consoles adds no jobs.
channels=${CHANNELS:-release beta-public}
cutoff=${CUTOFF:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}

fetch() {
	curl -fsSL --retry 3 --retry-max-time 45 \
		--connect-timeout 10 --max-time 30 \
		--get https://fw-update.ui.com/api/firmware \
		--data-urlencode 'filter=eq~~product~~unifi-dream' \
		--data-urlencode "filter=eq~~channel~~$1" \
		--data-urlencode 'sort=-created' \
		--data-urlencode 'limit=1000'
}

select_pairs() {
	jq -cer \
		--arg cutoff "${cutoff}" \
		--arg wanted "${SKU}" \
		--arg image "${UNIFI_OS_IMAGE}" \
		--arg channel "$1" \
		--argjson required "$2" \
		--from-file "${here}/pairs.jq"
}

pairs=()
for channel in ${channels}; do
	required=false
	if [[ ${channel} == release ]]; then
		required=true
	fi
	response=$(fetch "${channel}")
	selected=$(select_pairs "${channel}" "${required}" <<<"${response}")
	count=$(jq -er 'length' <<<"${selected}")
	noun=pairs
	if ((count == 1)); then
		noun=pair
	fi
	printf 'Firmware channel %s:\t%s %s\n' "${channel}" "${count}" "${noun}" >&2
	pairs+=("${selected}")
done

pinned=${PINNED:-${here}/pinned.json}
pinned_count=0
if [[ -s ${pinned} ]]; then pinned_count=$(jq -er 'length' "${pinned}"); fi
if ((pinned_count > 0)); then
	release=$(fetch release)
	selected=$(jq -cer \
		--arg cutoff "${cutoff}" \
		--arg wanted "${SKU}" \
		--arg image "${UNIFI_OS_IMAGE}" \
		--slurpfile loaded "${pinned}" \
		--from-file "${here}/pinned.jq" <<<"${release}")
	count=$(jq -er 'length' <<<"${selected}")
	noun=pairs
	if ((count == 1)); then
		noun=pair
	fi
	printf 'Pinned builds:\t\t%s %s\n' "${count}" "${noun}" >&2
	pairs+=("${selected}")
fi

matrix=$(jq -ces 'add' <<<"${pairs[*]}")

printf 'Firmware cutoff:\t\t%s\n' "${cutoff}" >&2
printf '%s\n' "${matrix}"
