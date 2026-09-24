package device

import (
	"testing"
)

func TestParseFirmwareLine(t *testing.T) {
	t.Parallel()
	firmware, discovery := parseFirmwareLine("UDMPRO.al324.v5.1.31.5acc35d.260819.1714")
	if firmware != "5.1.31" || discovery != "UDMPRO.al324.v5.1.31.5acc35d.260819.1714" {
		t.Fatalf("discovery line: firmware=%q discovery=%q", firmware, discovery)
	}
	firmware, discovery = parseFirmwareLine("5.1.31")
	if firmware != "5.1.31" || discovery != "" {
		t.Fatalf("version line: firmware=%q discovery=%q", firmware, discovery)
	}
	firmware, discovery = parseFirmwareLine("not-a-version")
	if firmware != "" || discovery != "" {
		t.Fatalf("rejected line: firmware=%q discovery=%q", firmware, discovery)
	}
}

func TestNormalizeSysID(t *testing.T) {
	t.Parallel()
	if got := NormalizeSysID("0xea15"); got != "ea15" {
		t.Fatalf("0xea15 = %q", got)
	}
	if got := NormalizeSysID("EA15"); got != "ea15" {
		t.Fatalf("EA15 = %q", got)
	}
	if got := NormalizeSysID("78:45:58:f8:ed:4f"); got != "" {
		t.Fatalf("accepted MAC %q", got)
	}
}

func TestKnownBoardMatchesWANTable(t *testing.T) {
	t.Parallel()
	for board := range wanByBoard {
		if !KnownBoard(board) {
			t.Fatalf("KnownBoard(%q) = false", board)
		}
	}
	if KnownBoard("UDM-Pro") || KnownBoard("UDMPROMAXX") {
		t.Fatal("hyphenated or unknown board accepted")
	}
}

func TestUbntDeviceInfoIgnoresOtherOptions(t *testing.T) {
	t.Parallel()
	if got := ubntDeviceInfo(t.Context(), "mac"); got != "" {
		t.Fatalf("mac option returned %q", got)
	}
	if got := ubntDeviceInfo(t.Context(), "summary"); got != "" {
		t.Fatalf("summary option returned %q", got)
	}
}
