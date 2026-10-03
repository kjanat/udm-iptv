package firmware_test

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"testing"
)

var (
	errUnexpectedArtifactEntry = errors.New("unexpected artifact archive entry")
	errArtifactDiffers         = errors.New("installed binary differs from shared build artifact")
)

func verifyArtifactArchive(input io.Reader, expected [sha256.Size]byte) error {
	archive := tar.NewReader(input)
	header, err := archive.Next()
	if err != nil {
		return fmt.Errorf("read artifact header: %w", err)
	}
	if header.Name != "udm-iptv" || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > 128<<20 {
		return errUnexpectedArtifactEntry
	}
	digest := sha256.New()
	if _, err := io.CopyN(digest, archive, header.Size); err != nil {
		return fmt.Errorf("hash installed artifact: %w", err)
	}
	if !bytes.Equal(digest.Sum(nil), expected[:]) {
		return errArtifactDiffers
	}
	return nil
}

func TestVerifyArtifactArchive(t *testing.T) {
	for _, test := range []struct {
		name     string
		entry    string
		kind     byte
		body     string
		truncate bool
		valid    bool
	}{
		{"matching", "udm-iptv", tar.TypeReg, "built binary", false, true},
		{"different", "udm-iptv", tar.TypeReg, "another binary", false, false},
		{"wrong name", "other", tar.TypeReg, "built binary", false, false},
		{"symlink", "udm-iptv", tar.TypeSymlink, "", false, false},
		{"empty", "udm-iptv", tar.TypeReg, "", false, false},
		{"truncated", "udm-iptv", tar.TypeReg, "built binary", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var buffer bytes.Buffer
			writer := tar.NewWriter(&buffer)
			if err := writer.WriteHeader(&tar.Header{Name: test.entry, Typeflag: test.kind, Size: int64(len(test.body))}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write([]byte(test.body)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			data := buffer.Bytes()
			if test.truncate {
				data = data[:512+len(test.body)-1]
			}
			err := verifyArtifactArchive(bytes.NewReader(data), sha256.Sum256([]byte("built binary")))
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t: %v", test.valid, err)
			}
		})
	}
}
