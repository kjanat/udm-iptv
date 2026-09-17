package firmware_test

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
)

var errUnexpectedConfigEntry = errors.New("unexpected configuration archive entry")

func readConfigArchive(input io.Reader) ([]byte, error) {
	archive := tar.NewReader(input)
	header, err := archive.Next()
	if err != nil {
		return nil, fmt.Errorf("read configuration archive header: %w", err)
	}
	if header.Name != "config.json" || header.Typeflag != tar.TypeReg || header.Size > 1<<20 {
		return nil, errUnexpectedConfigEntry
	}
	content, err := io.ReadAll(archive)
	if err != nil {
		return nil, fmt.Errorf("read configuration archive entry: %w", err)
	}

	return content, nil
}

func configArchive(t *testing.T, entry string, kind byte, size int64, complete bool) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: entry, Typeflag: kind, Size: size}); err != nil {
		t.Fatal(err)
	}
	if complete {
		if _, err := writer.Write([]byte("{}")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}

	return &buffer
}

func TestReadConfigArchive(t *testing.T) {
	for _, test := range []struct {
		name  string
		entry string
		kind  byte
		size  int64
		valid bool
	}{
		{"configuration", "config.json", tar.TypeReg, 2, true},
		{"other name", "other.json", tar.TypeReg, 2, false},
		{"symlink", "config.json", tar.TypeSymlink, 0, false},
		{"oversized", "config.json", tar.TypeReg, 2 << 20, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			content, err := readConfigArchive(configArchive(t, test.entry, test.kind, test.size, test.valid))
			if test.valid && (err != nil || string(content) != "{}") {
				t.Fatalf("valid configuration: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
}
