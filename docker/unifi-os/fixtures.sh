#!/bin/sh
set -eu

case ${1:-} in
	dhcp)
		# The package selects the parent and VLAN during installation.
		while true; do
			link=$(ip -oneline link show iptv 2>/dev/null | sed -n 's/^[0-9]\{1,\}: \([^:]*\):.*/\1/p')
			if [ -n "${link}" ]; then
				parent=${link#*@}
				vlan=$(ip -details link show "${link%@*}" | sed -n 's/.*vlan protocol [^ ]* id \([0-9]\{1,\}\).*/\1/p')
				if [ -n "${vlan}" ] && [ "${parent}" != "${link}" ]; then
					break
				fi
			fi
			sleep 1
		done
		# Recreate only our endpoint if the fixture itself is restarted.
		if ip link show iptv-peer >/dev/null 2>&1; then
			ip link delete iptv-peer
		fi
		ip link add link "peer-${parent}" name iptv-peer type vlan id "${vlan}"
		ip link set iptv-peer up
		ip address replace 198.51.100.1/24 dev iptv-peer
		# Keep the server in the unit's main process, with errors in its journal.
		exec dnsmasq --keep-in-foreground --conf-file=/dev/null --port=0 \
			--bind-interfaces --interface=iptv-peer --log-facility=- --log-dhcp \
			--dhcp-range=198.51.100.100,198.51.100.150,255.255.255.0,2h \
			--dhcp-option=3,198.51.100.1 \
			--pid-file=/run/udm-iptv-test/dnsmasq.pid
		;;
	lock)
		# Restore uses fuser to detect package-manager activity. Signal readiness
		# only after opening the file, before restore is allowed to start.
		exec 9>/var/lib/dpkg/lock
		systemd-notify --ready
		exec sleep "$2"
		;;
	*)
		echo "usage: $0 dhcp | lock <seconds>" >&2
		exit 2
		;;
esac
