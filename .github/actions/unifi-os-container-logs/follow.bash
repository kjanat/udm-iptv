#!/usr/bin/env bash
set -euo pipefail

: "${MODEL:?MODEL is required}"

prefix="udm-iptv-${MODEL}-"
since=$(date --utc +%s)

follow_journal() {
	local name=$1
	local n=0
	local started_at

	echo "Following journal in ${name}"
	while ((n < 120)); do
		if docker exec "${name}" test -S /run/systemd/private 2>/dev/null \
			&& started_at=$(docker exec "${name}" \
				cat /run/udm-iptv-test/started-at 2>/dev/null); then
			docker exec "${name}" journalctl \
				--follow \
				--since "${started_at}" \
				--output short-iso-precise \
				-u udm-iptv.service \
				-u udm-iptv-restore.service \
				--no-pager 2>&1 || true
			return
		fi
		if [[ $(docker inspect --format '{{.State.Running}}' "${name}" 2>/dev/null) != true ]]; then
			return
		fi
		sleep 0.25
		((n += 1))
	done
	echo "Journal did not become available in ${name}" >&2
}

docker events \
	--since "${since}" \
	--filter type=container \
	--filter event=start \
	--format '{{.Time}} {{.Actor.Attributes.name}}' \
	| while read -r _ name; do
		case ${name} in
			"${prefix}"*-from | "${prefix}"*-to)
				follow_journal "${name}"
				;;
			*) ;;
		esac
	done
