#!/usr/bin/env bash
set -euo pipefail

: "${MODEL:?MODEL is required}"

prefix="udm-iptv-${MODEL}-"
since=$(date --utc +%s)

docker events \
	--since "${since}" \
	--filter type=container \
	--filter event=start \
	--format '{{.Time}} {{.Actor.Attributes.name}}' \
	| while read -r started_at name; do
		case ${name} in
			"${prefix}"*-from | "${prefix}"*-to)
				echo "Following ${name}"
				docker logs --follow --timestamps --since "${started_at}" "${name}" 2>&1 || true
				;;
			*) ;;
		esac
	done
