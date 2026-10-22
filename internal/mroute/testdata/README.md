# Partial-startup VIF regression

These synthetic kernel-table fixtures reproduce the shape observed when IMProxy
starts with `wan`, `br0`, and a missing or addressless `br2`: the process remains alive, but
the kernel registers only `wan` and `br0`. Once `br2` has IPv4 and the proxy
restarts, all three VIFs register.

Addresses are documentation ranges, not identities from a router capture:
`wan=198.51.100.1`, `br0=192.0.2.1`, `br2=203.0.113.1`. The kernel's IPv4 hex
addresses are little-endian. Zero packet counters are intentional: registration
must pass for an idle, complete proxy and fail for a live, partial proxy.

`TestPartialStartupFixtures` runs without privileges or external binaries. The
matching real-binary regression lives in
`internal/service/proxy_interfaces_integration_test.go`; run it through
`bash tests/multicast/registration.sh` after building the pinned multicast image.
That test uses real kernel tables in private network/mount namespaces and verifies
preflight, activation, daemon startup, runtime monitoring, failure evidence, and
recovery. Source-build and shipped-firmware results remain distinct.
