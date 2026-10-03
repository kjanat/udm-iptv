# IMProxy downstream IPv4 querier election

Maintained patch for `haibbo/improxy` commit
`5a9d153d10e19ef835af9fc5f2a971799677a7ed`.
It changes the actual daemon, not a packet filter or a fixture-only substitute.

## Behavior

The original receive dispatcher ignores Membership Queries (`0x11`). Its
General Query timer therefore keeps transmitting after a lower-address querier
appears. The patch handles queries on the receiving downstream IPv4 interface,
suspends General Queries, refreshes the Other Querier Present timer, and resumes
queries when that timer expires. Higher, equal, and zero source addresses do not
win election. Each downstream interface has independent state.

V3 queries supply decoded robustness, query interval, and response interval;
zero robustness/interval fields use defaults. V2 response codes remain linear.
The default other-querier interval is 255 seconds. Timer deadlines retain the
half-decisecond resolution required by the response-interval term.

While passive, the daemon keeps learning memberships. It ignores new v2 Leaves
and does not prematurely shorten memberships for v3 report actions that would
require sending its own specific queries. Received specific queries lower only
existing affected timers; the v3 Suppress flag preserves those timers. An
already-started v2 last-member query sequence finishes after losing election,
as required by RFC 2236. Interface cleanup unregisters its query timer.

Two adjacent defects affect these paths and are also fixed: source records were
only partially zero-initialized, leaving query retransmission counters undefined;
the INCLUDE/BLOCK source loop used an uninitialized next pointer.

## Protocol and deployment scope

- V3 election uses General Queries, as clarified by
  [RFC 9776 section 6.6.2](https://www.rfc-editor.org/rfc/rfc9776.html#section-6.6.2).
  Configured v2 follows RFC 2236's broader Membership Query election event.
  A specific query's Last Member response time does not replace General Query
  response timing when updating the v2 election deadline.
- Membership timers retain this pinned source's RFC 3376 model:
  `RV * QI + QRI`, default 260 seconds. RFC 9776 changes that formula to
  `RV * QI + 2 * QRI`. This patch does **not** claim complete RFC 9776 compliance
  or automatic older-router version compatibility.
- Forwarding continues when another querier wins. This patch is for the
  explicitly selected **single forwarding proxy** deployment permitted by
  [RFC 4605 section 3](https://www.rfc-editor.org/rfc/rfc4605.html#section-3).
  It does not implement RFC 4605's default multiple-forwarder election policy.
  Do not use this forwarding behavior as loop protection with multiple proxies.
- MLD querier behavior and upstream kernel IGMP membership handling are unchanged.

## Build and direct regression tests

On Linux, provide a clean source export of the pinned commit:

```sh
sh patches/improxy/build.sh /path/to/improxy-source
```

The helper applies the patch without fuzz, runs the C regression tests, and
builds a static native executable at `/path/to/improxy-source/improxy`.
Override `CC` and `LDFLAGS` explicitly when needed. Nothing is installed or
started on a router. Static compilation reduces libc version dependencies;
it does not establish compatibility with a particular router kernel.

The same test harness compiles against **unmodified** source:

```sh
bash patches/improxy/test.sh /path/to/unmodified-pinned-source
```

It fails on the first lower-querier deadline assertion because the real receive
dispatcher ignores the valid query. With the patch, that assertion and the
remaining regression tests pass. Tests compile the actual production source;
linker wrappers replace clock, packet I/O, and kernel membership side effects.
No network namespace, root privileges, or network transmission is required.
Object files and the test executable live in an automatically removed temporary
directory. Core dumps are disabled for the intentional red assertion.

Coverage includes per-interface election, higher/equal/zero candidates, refresh
and recovery, v2/v3 timing encodings and defaults, valid zero response time,
specific-query source selection and Suppress handling, nonquerier report timer
behavior, the in-flight v2 leave exception, v2 specific-query election, and
multi-source INCLUDE/BLOCK traversal. Cleanup is checked through the real timer
registry. The fixture's ARM packet captures independently check on-wire election,
recovery, and multicast forwarding; these C tests are not a replacement for that
integration evidence.
