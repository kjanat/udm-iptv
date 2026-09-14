#!/usr/bin/env bash

set -euo pipefail

diagnostics_test_failure() {
	status=$?
	printf 'diagnostics test failed at line %s: %s\n' "${BASH_LINENO[0]}" "${BASH_COMMAND}" >&2
	exit "${status}"
}
trap diagnostics_test_failure ERR

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "${test_dir}"' EXIT
mkdir -p "${test_dir}/bin" "${test_dir}/proc/4242" "${test_dir}/output"
printf '10.207.100.210\n' >"${test_dir}/address-state"
real_date=$(command -v date)
real_jq=$(command -v jq)

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
echo "${UDM_IPTV_TEST_HOSTNAME:-private-router-name}"
EOF

cat >"${test_dir}/bin/uname" <<'EOF'
#!/bin/sh
[ "${UDM_IPTV_TEST_FAIL_UNAME:-false}" != true ] || exit 1
echo 'Linux 4.19.152-ui-alpine SMP aarch64'
EOF

cat >"${test_dir}/bin/systemctl" <<'EOF'
#!/bin/sh
if [ -n "${UDM_IPTV_TEST_STALL_PHASE:-}" ] \
	&& [ "${UDM_IPTV_TEST_STALL_PHASE}" = "${UDM_IPTV_CAPTURE_PHASE:-}" ]; then
	exec sleep 30
fi
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
    address=$(cat "${UDM_IPTV_ADDRESS_STATE}")
    echo "38: iptv inet ${address}/20 brd 10.207.111.255 scope global iptv"
    ;;
'-4 route get 1.1.1.1')
    echo "1.1.1.1 via 145.23.42.1 dev ppp0 src ${UDM_IPTV_TEST_DEVICE_ADDRESS:-145.23.42.7}"
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
printf '%s\n' "$*" >>"${UDM_IPTV_JOURNAL_CALLS}"
if [ -n "${UDM_IPTV_TEST_STALL_PHASE:-}" ] \
	&& [ "${UDM_IPTV_TEST_STALL_PHASE}" = "${UDM_IPTV_CAPTURE_PHASE:-}" ]; then
	exec sleep 30
fi
case " $* " in
*' --show-cursor '*)
	if [ -n "${UDM_IPTV_TEST_CURSOR_DELAY:-}" ]; then
		sleep "${UDM_IPTV_TEST_CURSOR_DELAY}"
	fi
	echo '-- cursor: s=diagnostics-test-cursor'
	exit
	;;
*' --after-cursor=s=diagnostics-test-cursor '*)
	awk 'BEGIN {
		for (i = 0; i < 2500; i++)
			printf "provider event %d abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789\n", i
	}'
	printf 'current assigned address %s\n' "$(cat "${UDM_IPTV_ADDRESS_STATE}")"
	exit
	;;
esac
lowercase_hostname=$(printf '%s\n' "${UDM_IPTV_TEST_HOSTNAME:-private-router-name}" | tr '[:upper:]' '[:lower:]')
cat <<LOGS
${UDM_IPTV_TEST_HOSTNAME:-private-router-name} udhcpc: lease of 10.207.100.210 obtained
${lowercase_hostname}.local qualified hostname
udhcpc: lease of 145.23.42.9 obtained; subscriber address 145.23.42.7 and static address 203.0.113.17
interface aa:bb:cc:dd:ee:ff joined 224.0.250.64 from provider 195.121.94.212
alternate MAC formats AA-BB-CC-DD-EE-FF and aabb.ccdd.eeff
provider 11.2.3.45 observed assigned ${UDM_IPTV_TEST_DEVICE_ADDRESS:-145.23.42.7}
event at 12:34:56 has identifier abc:def:
IPv6 endpoints 2001:db8::1, ::1, and ::ffff:192.0.2.128
udm-iptv.service remains active
LOGS
EOF

cat >"${test_dir}/bin/date" <<'EOF'
#!/bin/sh
if [ "$#" -eq 1 ] && [ "$1" = +%s ]; then
	echo 'diagnostics unexpectedly used the wall clock for a capture boundary' >&2
	exit 1
fi
exec "${UDM_IPTV_REAL_DATE}" "$@"
EOF

cat >"${test_dir}/bin/jq" <<'EOF'
#!/bin/sh
if [ "${UDM_IPTV_TEST_FAIL_RENDER:-false}" = true ] && [ "${1:-}" = -r ]; then
	printf 'partial rendered diagnostics\n'
	exit 1
fi
exec "${UDM_IPTV_REAL_JQ}" "$@"
EOF

chmod +x "${test_dir}/bin/"*

export PATH="${test_dir}/bin:${PATH}"
export UDM_IPTV_CONFIG_FILE="${test_dir}/config"
export UDM_IPTV_DIAGNOSTICS_DIR="${test_dir}/output"
export UDM_IPTV_PROC_DIR="${test_dir}/proc"
export UDM_IPTV_PROXY_CONFIG_FILE="${test_dir}/proxy-config"
export UDM_IPTV_JOURNAL_CALLS="${test_dir}/journal-calls"
export UDM_IPTV_ADDRESS_STATE="${test_dir}/address-state"
export UDM_IPTV_REAL_DATE="${real_date}"
export UDM_IPTV_REAL_JQ="${real_jq}"

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
grep -Fq '<ipv6-address>' <<<"${text}"
grep -Fq 'event at 12:34:56 has identifier abc:def:' <<<"${text}"
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
	AA-BB-CC-DD-EE-FF \
	aabb.ccdd.eeff \
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

collision_text=$(UDM_IPTV_TEST_HOSTNAME=iptv \
	UDM_IPTV_TEST_DEVICE_ADDRESS=1.2.3.4 ${diagnostics})
grep -Fq 'udm-iptv.service remains active' <<<"${collision_text}"
grep -Fq '11.2.3.45' <<<"${collision_text}"
grep -Fq '<router-hostname> udhcpc' <<<"${collision_text}"
grep -Fq 'assigned <device-address>' <<<"${collision_text}"
if grep -Fq 'assigned 1.2.3.4' <<<"${collision_text}"; then
	echo 'diagnostics leaked an assigned address during boundary-aware redaction' >&2
	exit 1
fi

hostname_case_text=$(UDM_IPTV_TEST_HOSTNAME=Private-Router ${diagnostics})
if grep -Fqi 'private-router' <<<"${hostname_case_text}"; then
	echo 'diagnostics leaked a case-variant router hostname' >&2
	exit 1
fi

if failure_output=$(UDM_IPTV_TEST_FAIL_UNAME=true ${diagnostics} 2>&1); then
	echo 'one-time diagnostics hid a snapshot producer failure' >&2
	exit 1
fi
grep -Fq 'error: Diagnostics failed unexpectedly' <<<"${failure_output}"

json=$(${diagnostics} --format json)
jq -e -s '
    length > 10
    and all(.[]; .schema == "io.github.udm-iptv.diagnostics.v1")
    and any(.[]; .kind == "section" and .section == "Multicast Routes")
' <<<"${json}" >/dev/null

missing_output_dir="${test_dir}/missing/output"
streamed_text=$(UDM_IPTV_DIAGNOSTICS_DIR="${missing_output_dir}" ${diagnostics})
grep -Fq 'Share-ready udm-iptv diagnostics.' <<<"${streamed_text}"
streamed_json=$(UDM_IPTV_DIAGNOSTICS_DIR="${missing_output_dir}" ${diagnostics} --format json)
jq -e -s 'length > 10 and all(.[]; .schema == "io.github.udm-iptv.diagnostics.v1")' \
	<<<"${streamed_json}" >/dev/null
if [[ -e ${missing_output_dir} ]]; then
	echo 'one-time diagnostics unexpectedly required an output directory' >&2
	exit 1
fi

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

unsafe_json=$(mktemp "${test_dir}/output/udm-iptv-diagnostics-unsafe-XXXXXX.jsonl")
unsafe_text="${test_dir}/output/udm-iptv-diagnostics-unsafe.txt"
unsafe_ready=$(mktemp "${test_dir}/output/udm-iptv-diagnostics-unsafe-XXXXXX.ready")
ln -s "${test_dir}/symlink-target" "${unsafe_text}"
worker_deadline=$(awk '{ printf "%.0f\n", ($1 + 1) * 1000 }' /proc/uptime)
if ${diagnostics} --worker 1 normal both "${unsafe_json}" "${unsafe_text}" - \
	"${worker_deadline}" "${unsafe_ready}" >/dev/null 2>&1; then
	echo 'diagnostics worker unexpectedly accepted a symlink output file' >&2
	exit 1
fi
rm -f "${unsafe_json}" "${unsafe_text}" "${unsafe_ready}"

capture_output=$(${diagnostics} --capture 8s)
json_file=$(sed -n 's/^Structured JSON Lines: //p' <<<"${capture_output}")
text_file=$(sed -n 's/^Share-ready text: //p' <<<"${capture_output}")
worker_pid=$(sed -n 's/^Diagnostics capture started in the background (PID \([0-9][0-9]*\)).$/\1/p' <<<"${capture_output}")
[[ -n ${json_file} && -n ${text_file} && -n ${worker_pid} ]]
[[ -f ${json_file} && ! -L ${json_file} ]]
[[ -f ${text_file} && ! -L ${text_file} ]]
kill -HUP "${worker_pid}"
printf '198.51.100.77\n' >"${UDM_IPTV_ADDRESS_STATE}"

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
	and any(.[]; .kind == "block" and .section == "Captured Service Logs"
	    and (.value | contains("provider event 2499")))
	and all(.[] | select(.kind == "sample");
	    (.network.routes | type) == "number"
	    and (.network.nat_packets | type) == "number"
	    and (.network.multicast_packets | type) == "number")
	and (last | .kind == "capture" and .name == "completed")
	and (last | .duration_seconds == 8 and .verbosity == "normal" and .format == "both")
' "${json_file}" >/dev/null
grep -Fq -- '--after-cursor=s=diagnostics-test-cursor' "${UDM_IPTV_JOURNAL_CALLS}"
if grep -Fq -- '--since' "${UDM_IPTV_JOURNAL_CALLS}"; then
	echo 'diagnostics capture unexpectedly selected logs using wall-clock time' >&2
	exit 1
fi
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
if grep -Fq '198.51.100.77' "${json_file}" "${text_file}"; then
	echo 'capture files leaked an address assigned after capture startup' >&2
	exit 1
fi
grep -Fq 'current assigned address <device-address>' "${json_file}" "${text_file}"

render_failure_output=$(UDM_IPTV_TEST_FAIL_RENDER=true ${diagnostics} --capture 8s --format text)
render_failure_text=$(sed -n 's/^Share-ready text: //p' <<<"${render_failure_output}")
[[ -n ${render_failure_text} ]]

render_failure_json=
render_failure_completed=false
for _ in {1..100}; do
	for candidate in "${test_dir}/output/"*.jsonl; do
		[[ -e ${candidate} && ${candidate} != "${json_file}" ]] || continue
		render_failure_json=${candidate}
		break
	done
	if [[ -n ${render_failure_json} && -s ${render_failure_text} ]] \
		&& jq -e -s 'last | .kind == "capture" and .name == "completed"' \
			"${render_failure_json}" >/dev/null 2>&1; then
		render_failure_completed=true
		break
	fi
	sleep 0.1
done
if [[ ${render_failure_completed} == false ]]; then
	echo 'diagnostics did not retain structured output after text rendering failed' >&2
	exit 1
fi
grep -Fq "Text rendering failed. Structured diagnostics remain at: ${render_failure_json}" \
	"${render_failure_text}"

short_capture_output=$(UDM_IPTV_TEST_STARTUP_DELAY=3 \
	${diagnostics} --capture 1s --format text --verbosity summary)
short_capture_text=$(sed -n 's/^Share-ready text: //p' <<<"${short_capture_output}")
[[ -n ${short_capture_text} ]]
short_capture_completed=false
for _ in {1..100}; do
	if [[ -s ${short_capture_text} ]] && grep -Fq 'Capture completed:' "${short_capture_text}"; then
		short_capture_completed=true
		break
	fi
	sleep 0.1
done
if [[ ${short_capture_completed} == false ]]; then
	echo 'short text-only diagnostics capture did not survive delayed acknowledgement' >&2
	exit 1
fi
grep -Fq 'Capture completed:' "${short_capture_text}"
if compgen -G "${test_dir}/output/*.ready" >/dev/null; then
	echo 'diagnostics capture left a readiness signal behind' >&2
	exit 1
fi

failed_capture_dir="${test_dir}/failed-output"
mkdir "${failed_capture_dir}"
if UDM_IPTV_DIAGNOSTICS_DIR="${failed_capture_dir}" \
	UDM_IPTV_MONOTONIC_FILE=/dev/null ${diagnostics} --capture 1s >/dev/null 2>&1; then
	echo 'diagnostics capture unexpectedly started without a monotonic clock' >&2
	exit 1
fi
if compgen -G "${failed_capture_dir}/*" >/dev/null; then
	echo 'failed diagnostics startup left output or readiness files behind' >&2
	exit 1
fi

slow_capture_output=$(UDM_IPTV_TEST_CURSOR_DELAY=1 ${diagnostics} --capture 8s --format json)
slow_json_file=$(sed -n 's/^Structured JSON Lines: //p' <<<"${slow_capture_output}")
[[ -n ${slow_json_file} && -f ${slow_json_file} ]]
slow_capture_completed=false
for _ in {1..100}; do
	if jq -e -s 'last | .kind == "capture" and .name == "completed"' \
		"${slow_json_file}" >/dev/null 2>&1; then
		slow_capture_completed=true
		break
	fi
	sleep 0.1
done
if [[ ${slow_capture_completed} == false ]]; then
	echo 'diagnostics capture failed after slow journal cursor acquisition' >&2
	exit 1
fi

for stalled_phase in initial sample final journal; do
	stall_started=${SECONDS}
	stalled_output=$(UDM_IPTV_TEST_STALL_PHASE=${stalled_phase} \
		${diagnostics} --capture 1s --format json)
	stalled_json=$(sed -n 's/^Structured JSON Lines: //p' <<<"${stalled_output}")
	[[ -n ${stalled_json} && -f ${stalled_json} ]]
	stalled_timed_out=false
	for _ in {1..40}; do
		if jq -e -s 'last | .kind == "capture" and .name == "timeout"' \
			"${stalled_json}" >/dev/null 2>&1; then
			stalled_timed_out=true
			break
		fi
		sleep 0.1
	done
	if [[ ${stalled_timed_out} == false ]]; then
		echo "${stalled_phase} collector was not bounded by the capture deadline" >&2
		exit 1
	fi
	if (( SECONDS - stall_started > 4 )); then
		echo "${stalled_phase} collector extended a one-second capture too far" >&2
		exit 1
	fi
done

UDM_IPTV_DIAGNOSTICS_HELPER="${diagnostics}" "${root}/udm-iptv" diagnose --help \
	| grep -Fq -- '--capture DURATION'
