#!/usr/bin/env bash

set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "${test_dir}"' EXIT
mkdir -p "${test_dir}/bin" "${test_dir}/proc/4242" "${test_dir}/output"

cat >"${test_dir}/config" <<'EOF'
IPTV_WAN_INTERFACE="eth8"
IPTV_WAN_VLAN="4"
IPTV_WAN_VLAN_INTERFACE="iptv"
IPTV_WAN_VLAN_MAC="aa:bb:cc:dd:ee:ff"
IPTV_WAN_DHCP="true"
IPTV_WAN_DHCP_OPTIONS="-O staticroutes -x 0xdeadbeef"
IPTV_WAN_STATIC_IP="203.0.113.17/24"
IPTV_WAN_RANGES="213.75.0.0/16 217.166.0.0/16 195.121.0.0/16"
IPTV_LAN_INTERFACES="br0"
IPTV_IGMPPROXY_PROGRAM="improxy"
IPTV_IGMPPROXY_IGMP_VERSION="3"
IPTV_IGMPPROXY_DISABLE_QUICKLEAVE="true"
IPTV_IGMPPROXY_DEBUG="false"
EOF

cat >"${test_dir}/proxy-config" <<'EOF'
# improxy configuration for udm-iptv
upstream iptv
downstream br0
EOF

printf 'improxy\0-c\0/var/run/igmpproxy.iptv.conf\0' >"${test_dir}/proc/4242/cmdline"

cat >"${test_dir}/bin/dpkg-query" <<'EOF'
#!/bin/sh
echo 'udm-iptv 4.3.0 (installed)'
EOF

cat >"${test_dir}/bin/ubnt-device-info" <<'EOF'
#!/bin/sh
case "$1" in
firmware) echo '5.1.31' ;;
model) echo 'UniFi Dream Machine Pro' ;;
*) exit 1 ;;
esac
EOF

cat >"${test_dir}/bin/hostname" <<'EOF'
#!/bin/sh
echo 'private-router-name'
EOF

cat >"${test_dir}/bin/uname" <<'EOF'
#!/bin/sh
echo 'Linux 4.19.152-ui-alpine SMP aarch64'
EOF

cat >"${test_dir}/bin/systemctl" <<'EOF'
#!/bin/sh
case "$*" in
is-system-running*) echo running ;;
is-enabled*) echo enabled ;;
is-active*) echo active ;;
show*MainPID*--value*) echo 4242 ;;
show*NRestarts*--value*) echo 0 ;;
show*SubState*MainPID*NRestarts*)
    printf 'SubState=running\nMainPID=4242\nNRestarts=0\n'
    ;;
show*)
    printf 'Id=udm-iptv.service\nLoadState=loaded\nUnitFileState=enabled\nActiveState=active\nSubState=running\nResult=success\nJob=\nMainPID=4242\nExecMainStatus=0\nNRestarts=0\n'
    ;;
list-jobs*) echo 'No jobs running.' ;;
*) exit 1 ;;
esac
EOF

cat >"${test_dir}/bin/ip" <<'EOF'
#!/bin/sh
case "$*" in
'-o link show dev iptv')
    echo '38: iptv@eth8: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP mode DEFAULT link/ether aa:bb:cc:dd:ee:ff'
    ;;
'-o -4 addr show dev iptv')
    echo '38: iptv inet 10.207.100.210/20 brd 10.207.111.255 scope global iptv'
    ;;
'-4 route get 1.1.1.1')
    echo '1.1.1.1 via 145.23.42.1 dev ppp0 src 145.23.42.7'
    ;;
'-4 addr show dev iptv')
    printf '38: iptv: <UP> mtu 1500 link/ether aa:bb:cc:dd:ee:ff\n    inet 10.207.100.210/20\n'
    ;;
'-4 route show dev iptv')
    echo '213.75.112.0/21 via 10.207.96.1 metric 238'
    ;;
'-s mroute show')
    printf '(195.121.94.212,224.0.250.64) Iif: iptv Oifs: br0 State: resolved\n  3621 packets, 1522441 bytes, Age 0.68\n(192.168.10.51,239.255.255.250) Iif: unresolved State: unresolved\n'
    ;;
*) exit 1 ;;
esac
EOF

cat >"${test_dir}/bin/iptables" <<'EOF'
#!/bin/sh
cat <<'TABLE'
Chain POSTROUTING (policy ACCEPT 10 packets, 1000 bytes)
num      pkts      bytes target     prot opt in     out     source               destination
1        1975     327680 MASQUERADE all  --  *      iptv    0.0.0.0/0            213.75.0.0/16
TABLE
EOF

cat >"${test_dir}/bin/journalctl" <<'EOF'
#!/bin/sh
cat <<'LOGS'
private-router-name udhcpc: lease of 10.207.100.210 obtained
udhcpc: lease of 145.23.42.9 obtained; subscriber address 145.23.42.7 and static address 203.0.113.17
interface aa:bb:cc:dd:ee:ff joined 224.0.250.64 from provider 195.121.94.212
LOGS
EOF

chmod +x "${test_dir}/bin/"*

export PATH="${test_dir}/bin:${PATH}"
export UDM_IPTV_CONFIG_FILE="${test_dir}/config"
export UDM_IPTV_DIAGNOSTICS_DIR="${test_dir}/output"
export UDM_IPTV_PROC_DIR="${test_dir}/proc"
export UDM_IPTV_PROXY_CONFIG_FILE="${test_dir}/proxy-config"

diagnostics="${root}/udm-iptv-diagnostics"

text=$(${diagnostics})
grep -Fq 'Share-ready udm-iptv diagnostics.' <<<"${text}"
grep -Fq 'Firmware: 5.1.31' <<<"${text}"
grep -Fq 'Proxy configured: improxy' <<<"${text}"
grep -Fq 'Proxy process: improxy' <<<"${text}"
grep -Fq 'IPTV default route: absent' <<<"${text}"
grep -Fq '195.121.94.212' <<<"${text}"
grep -Fq '213.75.112.0/21' <<<"${text}"
grep -Fq '<private-ip>' <<<"${text}"
grep -Fq '<device-address>' <<<"${text}"
grep -Fq '<multicast-group>' <<<"${text}"
grep -Fq '<mac>' <<<"${text}"
grep -Fq '<router-hostname>' <<<"${text}"
if grep -Fq '\1<' <<<"${text}"; then
	echo 'diagnostics contain a literal regex replacement marker' >&2
	exit 1
fi

for private_value in \
	10.207.100.210 \
	192.168.10.51 \
	145.23.42.7 \
	145.23.42.9 \
	203.0.113.17 \
	224.0.250.64 \
	239.255.255.250 \
	aa:bb:cc:dd:ee:ff \
	private-router-name \
	0xdeadbeef; do
	if grep -Fq "${private_value}" <<<"${text}"; then
		echo "diagnostics leaked ${private_value}" >&2
		exit 1
	fi
done

summary=$(${diagnostics} --verbosity summary)
grep -Fq '=== Service State ===' <<<"${summary}"
if grep -Fq '=== NAT Rules ===' <<<"${summary}" \
	|| grep -Fq '=== Service Logs' <<<"${summary}"; then
	echo 'summary diagnostics contained normal-detail sections' >&2
	exit 1
fi

debug=$(${diagnostics} --verbosity debug)
grep -Fq '=== Generated Proxy Configuration ===' <<<"${debug}"
grep -Fq 'upstream iptv' <<<"${debug}"

json=$(${diagnostics} --format json)
jq -e -s '
    length > 10
    and all(.[]; .schema == "io.github.udm-iptv.diagnostics.v1")
    and any(.[]; .kind == "section" and .section == "Multicast Routes")
' <<<"${json}" >/dev/null

if ${diagnostics} --format both >/dev/null 2>&1; then
	echo 'one-time diagnostics unexpectedly accepted --format both' >&2
	exit 1
fi
if ${diagnostics} --capture 0s >/dev/null 2>&1; then
	echo 'diagnostics unexpectedly accepted a zero duration' >&2
	exit 1
fi
if ${diagnostics} --capture 25h >/dev/null 2>&1; then
	echo 'diagnostics unexpectedly accepted a duration over 24h' >&2
	exit 1
fi
if ${diagnostics} --capture 999999999999999999h >/dev/null 2>&1; then
	echo 'diagnostics unexpectedly accepted an overflowing duration' >&2
	exit 1
fi
if ${diagnostics} --unknown >/dev/null 2>&1; then
	echo 'diagnostics unexpectedly accepted an unknown option' >&2
	exit 1
fi

capture_output=$(${diagnostics} --capture 1s)
json_file=$(sed -n 's/^Structured JSON Lines: //p' <<<"${capture_output}")
text_file=$(sed -n 's/^Share-ready text: //p' <<<"${capture_output}")
[[ -n ${json_file} && -n ${text_file} ]]

capture_completed=false
for _ in {1..100}; do
	if [[ -s ${text_file} ]] && jq -e -s 'last | .kind == "capture" and .name == "completed"' \
		"${json_file}" >/dev/null 2>&1; then
		capture_completed=true
		break
	fi
	sleep 0.1
done
if [[ ${capture_completed} == false ]]; then
	echo 'diagnostics capture did not complete within 10 seconds; final event:' >&2
	jq -s 'last' "${json_file}" >&2
	exit 1
fi

jq -e -s '
	any(.[]; .kind == "sample")
	and all(.[] | select(.kind == "sample");
	    (.network.routes | type) == "number"
	    and (.network.nat_packets | type) == "number"
	    and (.network.multicast_packets | type) == "number")
	and (last | .kind == "capture" and .name == "completed")
	and (last | .duration_seconds == 1 and .verbosity == "normal" and .format == "both")
' "${json_file}" >/dev/null
grep -Fq 'Capture completed:' "${text_file}"
json_mode=$(stat -c %a "${json_file}")
text_mode=$(stat -c %a "${text_file}")
[[ ${json_mode} == 600 ]]
[[ ${text_mode} == 600 ]]

if grep -Eq '10\.207\.100\.210|192\.168\.10\.51|145\.23\.42\.(7|9)|203\.0\.113\.17|224\.0\.250\.64|aa:bb:cc:dd:ee:ff|private-router-name|0xdeadbeef' \
	"${json_file}" "${text_file}"; then
	echo 'capture files contain unredacted diagnostic data' >&2
	exit 1
fi

UDM_IPTV_DIAGNOSTICS_HELPER="${diagnostics}" "${root}/udm-iptv" diagnose --help \
	| grep -Fq -- '--capture DURATION'
