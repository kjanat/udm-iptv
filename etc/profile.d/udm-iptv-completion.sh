# shellcheck shell=sh

# Load udm-iptv completion on UniFi OS, which does not include the general
# bash-completion loader.
if [ -n "${BASH_VERSION:-}" ] \
	&& [ -r /usr/share/bash-completion/completions/udm-iptv ]; then
	# shellcheck source=/dev/null
	. /usr/share/bash-completion/completions/udm-iptv
fi
