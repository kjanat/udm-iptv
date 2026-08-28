#!/bin/sh
set -eu

export container=docker

test_target=udm-iptv-test.target

printf 'APT::Get::Assume-Yes "true";\n' >/etc/apt/apt.conf.d/99yes
printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d
chmod 755 /usr/sbin/policy-rc.d

if command -v unifi-os >/dev/null 2>&1; then
	mv "$(command -v unifi-os)" /usr/sbin/unifi-os.real
fi

mkdir -p /usr/local/bin
stub=/usr/local/bin/improxy
printf '#!/bin/sh\nwhile true; do sleep 3600; done\n' >"${stub}"
chmod 755 "${stub}"
cp "${stub}" /usr/local/bin/igmpproxy

for iface in br0 eth0 eth1 eth2 eth3 eth4 eth8 eth9 eth18 eth19; do
	ip link add "${iface}" type dummy 2>/dev/null || true
	ip link set "${iface}" up 2>/dev/null || true
done

if [ -f /data/udm-iptv/udm-iptv-restore.service ]; then
	cp /data/udm-iptv/udm-iptv-restore.service /etc/systemd/system/udm-iptv-restore.service
	mkdir -p /etc/systemd/system/multi-user.target.wants
	ln -sf /etc/systemd/system/udm-iptv-restore.service \
		/etc/systemd/system/multi-user.target.wants/udm-iptv-restore.service
fi

if [ -x /lib/systemd/systemd ]; then
	exec /lib/systemd/systemd --system --unit="${test_target}"
fi
if [ -x /sbin/init ]; then
	exec /sbin/init --unit="${test_target}"
fi

echo "error: no systemd in this image" >&2
exit 1
