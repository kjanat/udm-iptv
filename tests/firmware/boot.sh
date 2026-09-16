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
for iface in br0 eth0 eth1 eth2 eth3 eth4 eth8 eth9 eth18 eth19; do
	if ! ip link show "${iface}" >/dev/null 2>&1; then
		ip link add "${iface}" type dummy
	fi
	ip link set "${iface}" up
done
ip address replace 192.0.2.1/24 dev br0

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
