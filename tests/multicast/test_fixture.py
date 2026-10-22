"""Fast offline checks for packets and evidence interpretation, not proxy conformance."""

import socket
import struct
import tempfile
import unittest
from pathlib import Path

from lab import election_result
from run import normalized_architecture
from wire import checksum, igmp, query, read_pcap


def ipv4(payload, options=b"\x94\x04\x00\x00"):
    header = (
        struct.pack(
            "!BBHHHBBH4s4s",
            0x45 + len(options) // 4,
            0,
            20 + len(options) + len(payload),
            0,
            0,
            1,
            2,
            0,
            socket.inet_aton("192.0.2.10"),
            socket.inet_aton("224.0.0.1"),
        )
        + options
    )
    return header[:10] + struct.pack("!H", checksum(header)) + header[12:] + payload


class PacketTests(unittest.TestCase):
    def test_known_general_queries(self):
        # Independent wire vectors: default QRI=10s, QRV=2, QI=125s.
        self.assertEqual(query(2), bytes.fromhex("11 64 ee 9b 00 00 00 00"))
        self.assertEqual(query(3), bytes.fromhex("11 64 ec 1e 00 00 00 00 02 7d 00 00"))
        for version in (2, 3):
            event = igmp(ipv4(query(version)))
            self.assertEqual(event["version"], version)
            self.assertEqual(event["source"], "192.0.2.10")
            self.assertTrue(event["router_alert"])

    def test_bad_checksum_cannot_support_election_claim(self):
        packet = bytearray(ipv4(query(3)))
        packet[-1] ^= 1
        with self.assertRaisesRegex(ValueError, "checksum"):
            igmp(bytes(packet))

    def test_truncated_packet_rejected(self):
        with self.assertRaisesRegex(ValueError, "truncated"):
            igmp(ipv4(query(3))[:-1])

    def test_missing_router_alert_is_not_invented(self):
        self.assertFalse(igmp(ipv4(query(2), options=b""))["router_alert"])

    def test_v3_report_records(self):
        report = bytes.fromhex("22 00 00 00 00 00 00 01 02 00 00 00 ef c0 00 01")
        report = report[:2] + struct.pack("!H", checksum(report)) + report[4:]
        self.assertEqual(igmp(ipv4(report))["groups"], ["239.192.0.1"])

    def test_capture_preserves_vlan_and_timestamp(self):
        frame = bytes(12) + bytes.fromhex("81 00 00 04 08 00") + ipv4(query(3))
        header = struct.pack("<IHHIIII", 0xA1B2C3D4, 2, 4, 0, 0, 65535, 1)
        record = struct.pack("<IIII", 100, 250000, len(frame), len(frame))
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "test.pcap"
            path.write_bytes(header + record + frame)
            self.assertEqual(read_pcap(path)[0]["time"], 100.25)
            path.write_bytes(header + record + frame[:-1])
            with self.assertRaisesRegex(ValueError, "truncated"):
                read_pcap(path)

    def test_native_architecture_aliases(self):
        self.assertEqual(normalized_architecture("aarch64"), "arm64")
        self.assertEqual(normalized_architecture("x86_64"), "amd64")
        self.assertNotEqual(normalized_architecture("x86_64"), "arm64")


class ElectionEvidenceTests(unittest.TestCase):
    def other(self, version=3):
        return [
            {
                "time": stamp,
                "version": version,
                "max_response_code": 100,
                "qrv": 2,
                "qqic": 125,
            }
            for stamp in (10, 41.25)
        ]

    def test_original_continued_queries_fail_lower_election(self):
        queries = [{"time": stamp} for stamp in (2, 33, 158, 283)]
        self.assertEqual(
            election_result("lower", queries, self.other(), 360)["status"], "fail"
        )

    def test_yield_and_correct_recovery_pass_both_versions(self):
        for version in (2, 3):
            queries = [{"time": stamp} for stamp in (2, 296.3)]
            result = election_result("recovery", queries, self.other(version), 360)
            self.assertEqual(result["status"], "pass")
            self.assertEqual(result["expected_recovery_at"], 296.25)

    def test_unpatched_schedule_fails_recovery(self):
        queries = [{"time": stamp} for stamp in (2, 33, 158, 283)]
        self.assertEqual(
            election_result("recovery", queries, self.other(), 360)["status"], "fail"
        )

    def test_permanently_silent_proxy_cannot_pass(self):
        self.assertEqual(
            election_result("recovery", [{"time": 2}], self.other(), 360)["status"],
            "fail",
        )

    def test_recovery_requires_refresh_from_second_query(self):
        queries = [{"time": stamp} for stamp in (2, 265, 296.3)]
        self.assertEqual(
            election_result("recovery", queries, self.other(), 360)["status"], "fail"
        )

    def test_early_end_or_missing_stimulus_is_inconclusive(self):
        for other, finish in ((self.other(), 280), ([], 360), (self.other()[:1], 360)):
            self.assertEqual(
                election_result("recovery", [], other, finish)["status"], "inconclusive"
            )

    def test_wrong_timer_values_cannot_support_recovery_claim(self):
        other = self.other()
        other[-1]["qqic"] = 10
        self.assertEqual(
            election_result("recovery", [], other, 360)["status"], "inconclusive"
        )

    def test_lower_and_higher_expect_opposite_behavior(self):
        queries = [{"time": 2}]
        self.assertEqual(
            election_result("lower", queries, self.other(), 360)["status"], "pass"
        )
        self.assertEqual(
            election_result("higher", queries, self.other(), 360)["status"], "fail"
        )


if __name__ == "__main__":
    unittest.main()
