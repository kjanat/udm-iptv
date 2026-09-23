# Diagnostics privacy

Local snapshots, captures and failure output retain addresses and raw logs. Capture files use mode 0600 and identify themselves as private. Keep these originals local. Verbosity controls sampling frequency; it does not control privacy or telemetry consent.

`udm-iptv diagnose export CAPTURE.jsonl` writes a separate sanitized JSON Lines stream to stdout. Add `--format text` for readable output. It requires no router access or telemetry, never uploads, and never rewrites its input. Export from the JSON Lines file created by `--format jsonl` or `--format both`.

Aliases remain consistent within one export and change across separate exports. Address families, multicast roles, counters and prefix lengths remain. Each event's `ipv4Order` lists IPv4 aliases in original numerical order, also rendered in text and the capture viewer, for querier election analysis. Synthetic addresses do not preserve subnet membership; do not infer topology or numerical election results directly from their octets.

Arbitrary log/marker messages and DHCP option payloads are omitted because they can contain credentials or identifying hostnames. Their timestamps and event types remain. Other opaque strings use aliases. The private input remains the source for full local investigation. JSON, text and the viewer use the same sanitized event data. Failure telemetry receives this sanitized attachment while local failure output retains detail; exception messages stay local.

Journal records arrive during capture rather than at finalization. The stream retains at most 10,000 records and 4 MiB of record input, with a 64 KiB limit per record; reaching a limit emits an explicit event. Closing the viewer leaves the capture worker running until its deadline. Capture markers and lifecycle acknowledgements remain beside the private capture.
