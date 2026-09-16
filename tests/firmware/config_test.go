package firmware_test

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"testing"
)

func readConfigArchive(input io.Reader) ([]byte, error) {
	archive := tar.NewReader(input)
	header, err := archive.Next()
	if err != nil {
		return nil, err
	}
	if header.Name != "config.json" || header.Typeflag != tar.TypeReg || header.Size > 1<<20 {
		return nil, errors.New("unexpected configuration archive entry")
	}

	return io.ReadAll(archive)
}

func TestReadConfigArchive(t *testing.T) {
	for _, test := range []struct {
		name  string
		kind  byte
		size  int64
		valid bool
	}{
		{"config.json", tar.TypeReg, 2, true},
		{"other.json", tar.TypeReg, 2, false},
		{"config.json", tar.TypeSymlink, 0, false},
		{"config.json", tar.TypeReg, 2 << 20, false},
	} {
		var buffer bytes.Buffer
		writer := tar.NewWriter(&buffer)
		if err := writer.WriteHeader(&tar.Header{Name: test.name, Typeflag: test.kind, Size: test.size}); err != nil {
			t.Fatal(err)
		}
		if test.valid {
			if _, err := writer.Write([]byte("{}")); err != nil {
				t.Fatal(err)
			}
			err := writer.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		content, err := readConfigArchive(&buffer)
		if test.valid && (err != nil || string(content) != "{}") {
			t.Fatalf("valid configuration: %v", err)
		}
		if !test.valid && err == nil {
			t.Fatal("invalid archive accepted")
		}
	}
}
