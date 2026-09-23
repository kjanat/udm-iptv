package firmware

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func fileRootFixture(used uint64) []byte {
	header := slices.Clone(fixture(false)[:ubntHeaderSize])
	record := make([]byte, fileRecordHeaderSize+squashfsSuperblockSize+crcFooterSize)
	copy(record, "FILErootfs")
	binary.BigEndian.PutUint32(record[48:52], squashfsSuperblockSize)
	copy(record[fileRecordHeaderSize:], "hsqs")
	binary.LittleEndian.PutUint64(record[fileRecordHeaderSize+40:], used)
	footer := len(record) - crcFooterSize
	binary.BigEndian.PutUint32(record[footer:], crc32.ChecksumIEEE(record[:footer]))

	return append(header, record...)
}

func TestExtractSquashfsFILE(t *testing.T) {
	t.Parallel()
	image := fileRootFixture(squashfsSuperblockSize)
	var output bytes.Buffer
	if err := Extract(bytes.NewReader(image), int64(len(image)), &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() != squashfsSuperblockSize || !bytes.HasPrefix(output.Bytes(), []byte("hsqs")) {
		t.Fatal("FILE payload was not extracted")
	}
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"length exceeds FILE even with trailing data", append(fileRootFixture(squashfsSuperblockSize+1), make([]byte, 128)...)},
		{"truncated FILE footer", image[:len(image)-crcFooterSize]},
		{"corrupt FILE CRC", append(slices.Clone(image[:len(image)-crcFooterSize]), make([]byte, crcFooterSize)...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := Extract(bytes.NewReader(test.data), int64(len(test.data)), io.Discard); err == nil {
				t.Fatal("invalid FILE rootfs accepted")
			}
		})
	}
}

func parityCatalog(t *testing.T, board string, cutoff time.Time) string {
	t.Helper()
	entries := make([]catalogRelease, 0, 3)
	for index, version := range []string{"5.1.9", "5.1.33", "7.0.0"} {
		entry := catalogRelease{Platform: board, Channel: channelRelease, Version: "v" + version + "+1", Created: cutoff.Add(-time.Hour), SHA256: strings.Repeat("a", 64)}
		if index == 2 {
			entry.Created = cutoff.Add(time.Hour)
		}
		entry.Links.Data.Href = "https://fw-download.ubnt.com/firmware.bin"
		entries = append(entries, entry)
	}
	var document catalogDocument
	document.Embedded.Firmware = entries
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	return string(encoded)
}

func TestCatalogDiscoversNewPlatformsAndAliases(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct{ board, requested, want string }{
		{"UCGMAX", "all", "ucgmax"},
		{"UCGMAX", "ucgmax", "ucgmax"},
		{"UDMPROSE", "all", "udmse"},
		{"UDMPROSE", "udmprose", "udmprose"},
		{"UDMPROSE", "udmse", "udmse"},
		{"NEWBOARD", "all", "newboard"},
	} {
		matrix, err := SelectCatalog(strings.NewReader(parityCatalog(t, test.board, cutoff)), testImage, test.requested, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		if len(matrix.Include) != 1 {
			t.Fatalf("unexpected discovered matrix: %+v", matrix)
		}
		pair := matrix.Include[0]
		if pair.Model != test.want || pair.Firmwares[0].Board != test.board || pair.Firmwares[1].Version != "5.1.33" {
			t.Fatalf("wrong platform or cutoff: %+v", pair)
		}
		if err := ValidatePair(pair.Model, pair.Firmwares); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublishedDiscoversNewPlatformsAndAliases(t *testing.T) {
	t.Parallel()
	tags := "ucgmax-5.1.9\nucgmax-5.1.33\nudmprose-5.1.9\nudmse-5.1.33\nUDMPRO-9.0.0\ninvalid-tag"
	matrix, err := Published(tags, testImage)
	if err != nil {
		t.Fatal(err)
	}
	if len(matrix.Include) != 2 || matrix.Include[0].Model != "udmse" || matrix.Include[1].Model != "ucgmax" {
		t.Fatalf("published platforms = %+v", matrix)
	}
	if !strings.HasSuffix(matrix.Include[0].From, ":udmprose-5.1.9") || !strings.HasSuffix(matrix.Include[0].To, ":udmse-5.1.33") {
		t.Fatalf("actual alias tags lost: %+v", matrix.Include[0])
	}
}

func TestPinnedCatalogPreservesMetadataAndCutoff(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	track, found := TrackNamed("pinned")
	if !found {
		t.Fatal("pinned track unavailable")
	}
	matrix, err := track.SelectCatalog(strings.NewReader(parityCatalog(t, "UDMPRO", cutoff)), testImage, "udmpro", cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(matrix.Include) != 1 {
		t.Fatalf("pinned matrix = %+v", matrix)
	}
	pair := matrix.Include[0]
	if pair.Firmwares[0].Version != "5.1.33" || pair.Firmwares[1].Version != "6.0.7" || pair.Firmwares[1].SHA256 != "97f20eae85f5cd0fbd9390f2187cb1d28f803890caae96ce617f4ff0ff56d44f" {
		t.Fatalf("pin or cutoff changed: %+v", pair)
	}
	if pair.From != testImage+":udmpro-pinned-5.1.33" || pair.To != testImage+":udmpro-pinned-6.0.7" || track.latestAlias() != "pinned" {
		t.Fatalf("pinned images overlap stable aliases: %+v", pair)
	}
}

func TestPinnedPublishedReusesLegacyImages(t *testing.T) {
	t.Parallel()
	track := PinnedTrack()
	for _, test := range []struct{ tags, from, to string }{
		{"udmpro-5.1.33\nudmpro-6.0.7", "udmpro-5.1.33", "udmpro-6.0.7"},
		{"udmpro-5.1.33\nudmpro-6.0.7\nudmpro-pinned-5.1.33\nudmpro-pinned-6.0.7", "udmpro-pinned-5.1.33", "udmpro-pinned-6.0.7"},
	} {
		matrix, err := track.Published(test.tags, testImage)
		if err != nil {
			t.Fatal(err)
		}
		if len(matrix.Include) != 1 || matrix.Include[0].From != testImage+":"+test.from || matrix.Include[0].To != testImage+":"+test.to {
			t.Fatalf("pinned published = %+v", matrix)
		}
	}
	matrix, err := track.Published("udmpro-5.1.9\nudmpro-5.1.33", testImage)
	if err != nil || len(matrix.Include) != 0 {
		t.Fatalf("unpublished pin should be omitted: %+v, %v", matrix, err)
	}
}

func TestPinnedPublishingPreservesStableAliases(t *testing.T) {
	t.Parallel()
	pair := pairFor("udmpro")
	pair[1].Version = "6.0.7"
	track := PinnedTrack()
	runner := &fakeRunner{visibility: "public", fingerprints: map[string]string{}}
	for _, release := range pair {
		runner.fingerprints[testImage+":"+track.versionTag("udmpro", release.Version)] = Fingerprint("udmpro", release)
	}
	pipeline := Pipeline{Track: track, Runner: runner, Images: runner, Log: io.Discard}
	if err := pipeline.Build(t.Context(), testImage, "udmpro", t.TempDir(), pair); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Publish(t.Context(), testImage, "udmpro", pair); err != nil {
		t.Fatal(err)
	}
	if runner.count("docker push "+testImage+":pinned") != 1 {
		t.Fatal("pinned alias was not published")
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "latest") || strings.Contains(call, "beta") {
			t.Fatalf("pinned publishing changed another track: %s", call)
		}
	}
}
