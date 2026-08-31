#!/bin/sh

set -eu

if [ "${1:-}" != "--inside" ]; then
	hook=$(readlink -f "$(dirname "$0")/../udhcpc.hook")
	user_id=$(id -u)
	if [ "${user_id}" -eq 0 ]; then
		exec unshare --net "$0" --inside "${hook}"
	fi
	exec unshare --user --map-root-user --net "$0" --inside "${hook}"
fi

hook=$2
interface=udm-iptv-test
ip=192.0.2.10
mask=24
subnet=255.255.255.0
broadcast=192.0.2.255

ip link add "${interface}" type dummy
link=$(ip -o link show dev "${interface}")
ifindex=${link%%:*}
metric=$((200 + ifindex))

run_hook() {
	event=$1
	staticroutes=$2
	router=$3
	env -i \
		PATH=/usr/bin:/bin:/usr/sbin:/sbin \
		interface="${interface}" \
		ip="${ip}" \
		mask="${mask}" \
		subnet="${subnet}" \
		broadcast="${broadcast}" \
		staticroutes="${staticroutes}" \
		router="${router}" \
		"${hook}" "${event}"
}

routes() {
	ip -4 route show dev "${interface}" proto dhcp
}

fail() {
	echo "error: $1" >&2
	echo "DHCP routes:" >&2
	routes >&2
	exit 1
}

assert_contains() {
	needle=$1
	actual=$(routes)
	case "${actual}" in
		*"${needle}"*) ;;
		*) fail "missing route: ${needle}" ;;
	esac
}

assert_not_contains() {
	needle=$1
	actual=$(routes)
	case "${actual}" in
		*"${needle}"*) fail "unexpected route: ${needle}" ;;
		*) ;;
	esac
}

assert_route_count() {
	want=$1
	actual=$(routes)
	if [ -n "${actual}" ]; then
		have=$(printf '%s\n' "${actual}" | wc -l)
	else
		have=0
	fi
	[ "${have}" -eq "${want}" ] || fail "expected ${want} routes, found ${have}"
}

# Option 121 takes precedence over the Router option. Host bits in its
# destination are cleared, and a zero gateway is installed as an on-link route.
run_hook bound \
	"198.51.100.130/25 192.0.2.1 203.0.113.42/24 0.0.0.0" \
	"192.0.2.254"
assert_route_count 2
assert_contains "198.51.100.128/25 via 192.0.2.1 metric ${metric}"
assert_contains "203.0.113.0/24 scope link metric $((metric + 1))"
assert_not_contains "default"
ip -4 route add 10.0.0.0/8 dev "${interface}" proto static

# Repeated renewals remain idempotent.
run_hook renew \
	"198.51.100.130/25 192.0.2.1 203.0.113.42/24 0.0.0.0" \
	"192.0.2.254"
run_hook renew \
	"198.51.100.130/25 192.0.2.1 203.0.113.42/24 0.0.0.0" \
	"192.0.2.254"
assert_route_count 2

# A changed lease removes stale routes and replaces changed gateways.
run_hook renew "198.51.100.130/25 192.0.2.2" "192.0.2.254"
assert_route_count 1
assert_contains "198.51.100.128/25 via 192.0.2.2 metric ${metric}"
assert_not_contains "192.0.2.1"
assert_not_contains "203.0.113.0/24"

# Without option 121, the Router option supplies the default route.
run_hook renew "" "192.0.2.254"
assert_route_count 1
assert_contains "default via 192.0.2.254 metric ${metric}"

# A lease without route options removes routes from the previous lease.
run_hook renew "" ""
assert_route_count 0
manual_routes=$(ip -4 route show dev "${interface}" proto static)
case "${manual_routes}" in
	*"10.0.0.0/8"*) ;;
	*) fail "renew removed a manually configured route" ;;
esac

run_hook deconfig "" ""
remaining_addresses=$(ip -4 addr show dev "${interface}" scope global)
[ -z "${remaining_addresses}" ] \
	|| fail "deconfig left an address on ${interface}"

echo "udhcpc route tests passed"
