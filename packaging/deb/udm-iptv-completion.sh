# shellcheck shell=sh
# UniFi OS has no bash-completion loader.
if [ -n "${BASH_VERSION:-}" ]; then
	if [ -r /etc/bash_completion.d/udm-iptv ]; then
		# shellcheck source=/dev/null
		. /etc/bash_completion.d/udm-iptv
	elif [ -r /usr/share/bash-completion/completions/udm-iptv ]; then
		# shellcheck source=/dev/null
		. /usr/share/bash-completion/completions/udm-iptv
	fi
fi
