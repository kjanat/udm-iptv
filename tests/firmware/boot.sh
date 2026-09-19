#!/bin/sh
set -eu

export container=docker

# Keep the firmware's own /etc underneath the persistent writable overlay.
lower=/run/udm-iptv-test/etc-lower
overlay=/var/lib/udm-iptv-test/etc-overlay
mkdir -p "${lower}" "${overlay}/upper" "${overlay}/work"
mount --bind /etc "${lower}"
mount --bind -o remount,ro "${lower}"
mount -t overlay overlay -o "lowerdir=${lower},upperdir=${overlay}/upper,workdir=${overlay}/work" /etc

# Only hardware interfaces are simulated. The service and proxy are real.
# eth8 is one end of a veth pair so a DHCP server can answer on the other.
for iface in br0 eth0 eth1 eth2 eth3 eth4 eth9 eth18 eth19; do
	if ! ip link show "${iface}" >/dev/null 2>&1; then
		ip link add "${iface}" type dummy
	fi
	ip link set "${iface}" up
done
if ! ip link show eth8 >/dev/null 2>&1; then
	ip link add eth8 type veth peer name eth8-peer
fi
ip link set eth8 up
ip link set eth8-peer up
ip address replace 192.0.2.1/24 dev br0

# A provider-side DHCP server on VLAN 4 behind eth8: one lease pool, an
# RFC3442 route and no router option, the way KPN's IPTV network answers.
if [ "${UDM_IPTV_TEST_DHCP:-0}" = 1 ]; then
	command -v dnsmasq >/dev/null || {
		echo "udm-iptv-test: dnsmasq is missing from this firmware" >&2
		exit 1
	}
	if ! ip link show eth8-peer.4 >/dev/null 2>&1; then
		ip link add link eth8-peer name eth8-peer.4 type vlan id 4
	fi
	ip link set eth8-peer.4 up
	ip address replace 10.207.64.1/20 dev eth8-peer.4
	dnsmasq --interface=eth8-peer.4 --bind-interfaces --except-interface=lo --port=0 \
		--dhcp-range=10.207.64.10,10.207.64.20,1h --dhcp-authoritative \
		--dhcp-option=option:classless-static-route,213.75.112.0/21,10.207.64.1 \
		--dhcp-option=option:router \
		--log-dhcp --log-facility=/run/udm-iptv-test/dnsmasq.log --pid-file=/run/udm-iptv-test/dnsmasq.pid
fi

# Exercise the preserved real proxy when firmware no longer supplies it.
if [ "${UDM_IPTV_TEST_ERASE_PROXY:-0}" = 1 ]; then
	if proxy=$(command -v improxy); then
		mv "${proxy}" /run/udm-iptv-test/firmware-improxy
	fi
fi

# Honor existing enablement. Never install, enable or start udm-iptv here.
target=udm-iptv-test.target
mkdir -p "/run/systemd/system/${target}.wants"
for enabled in /etc/systemd/system/multi-user.target.wants/*; do
	[ -L "${enabled}" ] || continue
	name=$(basename "${enabled}")
	firmware_link="${lower}/systemd/system/multi-user.target.wants/${name}"
	if [ -e "${firmware_link}" ] || [ -L "${firmware_link}" ]; then
		continue
	fi
	unit=$(readlink -f "${enabled}")
	if [ -e "${enabled}" ]; then
		case ${unit} in
			/etc/systemd/system/*) ;;
			*) continue ;;
		esac
	fi
	ln -sf "${unit}" "/run/systemd/system/${target}.wants/${name}"
done

if [ -x /lib/systemd/systemd ]; then
	exec /lib/systemd/systemd --system --unit="${target}"
fi
exec /sbin/init --unit="${target}"
