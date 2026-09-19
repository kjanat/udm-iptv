#!/bin/sh
set -eu

export container=docker

test_target=udm-iptv-test.target
etc_lower=/run/udm-iptv-test/etc-lower
etc_overlay=/var/lib/udm-iptv-test/etc-overlay
started_at=/run/udm-iptv-test/started-at

# UniFi OS keeps a writable overlay across ordinary boots and firmware updates.
# Reuse only that overlay between the extracted roots so the new firmware still
# supplies its own /etc while package-created files survive naturally.
mkdir -p "${etc_lower}" "${etc_overlay}/upper" "${etc_overlay}/work"
date -u '+%Y-%m-%d %H:%M:%S UTC' >"${started_at}"
mount --bind /etc "${etc_lower}"
mount --bind -o remount,ro "${etc_lower}"
mount -t overlay overlay \
	-o "lowerdir=${etc_lower},upperdir=${etc_overlay}/upper,workdir=${etc_overlay}/work" \
	/etc

printf 'APT::Get::Assume-Yes "true";\n' >/etc/apt/apt.conf.d/99yes

# The extracted firmware root ships no machine-id, and dbus-daemon exits when it
# cannot read one.
if [ ! -s /etc/machine-id ]; then
	if command -v systemd-machine-id-setup >/dev/null 2>&1; then
		systemd-machine-id-setup
	else
		tr -d - </proc/sys/kernel/random/uuid >/etc/machine-id
	fi
fi

if unifi_os=$(command -v unifi-os); then
	mv "${unifi_os}" /usr/sbin/unifi-os.real
fi

# A dummy interface discards everything it transmits, so a DHCP request on one
# reaches no server. Each WAN candidate is a veth pair instead, and its peer
# carries the answering end. The package enumerates br* and eth0*.
for iface in br0 eth0 eth1 eth2 eth3 eth4 eth8 eth9 eth18 eth19; do
	ip link add "${iface}" type veth peer name "peer-${iface}" 2>/dev/null || true
	ip link set "${iface}" up 2>/dev/null || true
	ip link set "peer-${iface}" up 2>/dev/null || true
done
ip address replace 192.0.2.1/24 dev br0

# The profile picks its WAN port and VLAN through debconf, so both are only
# known once the daemon has created the VLAN interface.
serve_dhcp() {
	vlan_link=$1
	parent=${vlan_link#*@}
	vlan=$(ip -details link show "${vlan_link%@*}" | sed -n 's/.*vlan protocol [^ ]* id \([0-9]\{1,\}\).*/\1/p')
	if [ -z "${vlan}" ] || [ "${parent}" = "${vlan_link}" ]; then
		return 0
	fi
	ip link add link "peer-${parent}" name iptv-peer type vlan id "${vlan}"
	ip link set iptv-peer up
	ip address replace 198.51.100.1/24 dev iptv-peer
	dnsmasq --port=0 --bind-interfaces --interface=iptv-peer \
		--dhcp-range=198.51.100.100,198.51.100.150,255.255.255.0,2h \
		--dhcp-option=3,198.51.100.1 \
		--pid-file=/run/udm-iptv-test/dnsmasq.pid
	touch /run/udm-iptv-test/dhcp-served
}

(
	while true; do
		link=$(ip -oneline link show iptv 2>/dev/null | sed -n 's/^[0-9]\{1,\}: \([^:]*\):.*/\1/p')
		if [ -n "${link}" ] && [ ! -e /run/udm-iptv-test/dhcp-served ]; then
			serve_dhcp "${link}"
		fi
		sleep 1
	done
) &

if [ -n "${UDM_IPTV_TEST_LOCK_SECONDS:-}" ]; then
	(
		exec 9>/var/lib/dpkg/lock
		sleep "${UDM_IPTV_TEST_LOCK_SECONDS}"
	) &
fi

# The extracted root cannot reach the hardware-dependent multi-user target.
# Pull the enabled package service and persistent /etc units into the small test
# target without copying, enabling or starting any unit on its behalf. Broken
# links identify units removed with the package; other intact firmware units
# below /lib are deliberately left to the hardware-dependent production target.
mkdir -p "/run/systemd/system/${test_target}.wants"
for enabled in /etc/systemd/system/multi-user.target.wants/*; do
	[ -L "${enabled}" ] || continue
	name=$(basename "${enabled}")
	firmware_link="${etc_lower}/systemd/system/multi-user.target.wants/${name}"
	if [ -e "${firmware_link}" ] || [ -L "${firmware_link}" ]; then
		continue
	fi
	# Some readlink builds fail on a dangling link instead of printing its target.
	if ! target=$(readlink -f "${enabled}"); then
		echo "harness: cannot resolve ${enabled}" >&2
		continue
	fi
	if [ -e "${enabled}" ]; then
		case ${target} in
			/etc/systemd/system/* | */systemd/system/udm-iptv.service) ;;
			*) continue ;;
		esac
	fi
	ln -sf "${target}" "/run/systemd/system/${test_target}.wants/${name}"
done

# systemd logs to the journal by default, which dies with the container, so a
# boot failure leaves nothing behind for docker logs to show.
echo "harness: starting systemd for ${test_target}" >&2
if [ -x /lib/systemd/systemd ]; then
	exec /lib/systemd/systemd --system --log-target=console --unit="${test_target}"
fi
if [ -x /sbin/init ]; then
	exec /sbin/init --log-target=console --unit="${test_target}"
fi

echo "error: no systemd in this image" >&2
exit 1
