package firmware

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math"
	"slices"
	"testing"
)

func TestRootfsRangeRejectsOverflow(t *testing.T) {
	for _, size := range []int64{-1, math.MaxInt64, maximumFirmwareSize + 1} {
		if err := Extract(bytes.NewReader(nil), size, io.Discard); err == nil {
			t.Fatal("invalid image size accepted")
		}
	}
	for _, length := range []uint64{0, 95, math.MaxInt64, math.MaxUint64} {
		image := fixture(false)
		binary.LittleEndian.PutUint64(image[len(image)-56:], length)
		if err := Extract(bytes.NewReader(image), int64(len(image)), io.Discard); err == nil {
			t.Fatal("invalid rootfs length accepted")
		}
	}
	if _, _, err := rootfsRange(bytes.NewReader(nil), math.MaxInt64, math.MaxInt64); err == nil {
		t.Fatal("overflowed partition accepted")
	}
}

type failingExtractWriter struct{ err error }

func (writer failingExtractWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestExtractPreservesOutputError(t *testing.T) {
	image := fixture(false)
	want := errors.New("disk full")
	if err := Extract(bytes.NewReader(image), int64(len(image)), failingExtractWriter{want}); !errors.Is(err, want) {
		t.Fatalf("lost cause: %v", err)
	}
}

func FuzzExtract(f *testing.F) {
	f.Add(fixture(false))
	f.Add(fixture(true))
	f.Fuzz(func(_ *testing.T, image []byte) {
		_ = Extract(bytes.NewReader(image), int64(len(image)), io.Discard)
	})
}

func fixture(nested bool) []byte {
	header := make([]byte, 0x108)
	copy(header, "UBNT")
	binary.BigEndian.PutUint32(header[0x104:], crc32.ChecksumIEEE(header[:0x104]))
	partition := make([]byte, 0x38+96)
	copy(partition, "PARTrootfs")
	copy(partition[0x38:], "hsqs")
	binary.LittleEndian.PutUint64(partition[0x38+40:], 96)
	payload := []byte("data")
	if nested {
		payload = partition
	}
	if len(payload) > math.MaxUint32 {
		panic("fixture payload exceeds a FILE record's uint32 length field")
	}
	record := make([]byte, 0x38, 0x38+len(payload)+8)
	copy(record, "FILE../../bad")
	binary.BigEndian.PutUint32(record[48:], uint32(len(payload))) //nolint:gosec // Guarded above; gosec's check is syntactic and misses it.
	record = append(record[:0x38:0x38], payload...)
	checksum := crc32.ChecksumIEEE(record)
	record = append(slices.Clip(record), make([]byte, 8)...)
	binary.BigEndian.PutUint32(record[len(record)-8:], checksum)
	result := make([]byte, 0, len(header)+len(record)+len(partition))
	result = append(result, header...)
	result = append(result, record...)
	if !nested {
		result = append(result, partition...)
	}

	return result
}

func TestExtract(t *testing.T) {
	for _, nested := range []bool{false, true} {
		image := fixture(nested)
		var output bytes.Buffer
		err := Extract(bytes.NewReader(image), int64(len(image)), &output)
		if err != nil {
			t.Fatal(err)
		}
		if output.Len() != 96 || string(output.Bytes()[:4]) != "hsqs" {
			t.Fatal("incorrect rootfs")
		}
	}
	for _, name := range []string{"short", "header", "file", "length", "rootfs"} {
		t.Run(name, func(t *testing.T) {
			image := fixture(false)
			switch name {
			case "short":
				image = image[:100]
			case "header":
				image[5] = 1
			case "file":
				image[0x140] = 0
			case "length":
				binary.LittleEndian.PutUint64(image[len(image)-56:], 1000)
			case "rootfs":
				image = image[:len(image)-96]
			}
			err := Extract(bytes.NewReader(image), int64(len(image)), io.Discard)
			if err == nil {
				t.Fatal("malformed firmware accepted")
			}
		})
	}
}

func TestPartitionAcrossReadBoundary(t *testing.T) {
	data := make([]byte, (1<<20)+100)
	copy(data[(1<<20)-5:], "PARTrootfs")
	offset, err := findRootfs(bytes.NewReader(data), 0, int64(len(data)))
	if err != nil || offset != (1<<20)-5 {
		t.Fatalf("boundary marker: %d %v", offset, err)
	}
}
