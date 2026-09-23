package firmware

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
)

const maximumFirmwareSize int64 = 4 << 30

const (
	// ubntHeaderSize is the fixed UBNT image header, ending in a CRC32 trailer.
	ubntHeaderSize = 0x108
	// fileRecordHeaderSize is a FILE record's fixed header, before its payload.
	fileRecordHeaderSize = 0x38
	// crcFooterSize is the trailing CRC32 that follows a FILE record's payload.
	crcFooterSize = 8
	// squashfsSuperblockSize is the "hsqs" superblock read from a PARTrootfs entry.
	squashfsSuperblockSize = 96
	// rootfsScanBufferSize is the read chunk used to search for "PARTrootfs".
	rootfsScanBufferSize = 1 << 20
)

var (
	errTruncatedHeader      = errors.New("truncated firmware header")
	errFirmwareTooLarge     = errors.New("firmware exceeds four GiB")
	errNotUBNTImage         = errors.New("not a UBNT firmware image")
	errHeaderCRCMismatch    = errors.New("firmware header CRC mismatch")
	errNoRootfs             = errors.New("no root filesystem found")
	errMissingSuperblock    = errors.New("missing squashfs superblock")
	errInvalidRootfsLength  = errors.New("invalid squashfs length")
	errRootfsExceedsImage   = errors.New("squashfs exceeds firmware size")
	errTruncatedFileHeader  = errors.New("truncated FILE header")
	errTruncatedFilePayload = errors.New("truncated FILE payload")
	errFileCRCMismatch      = errors.New("FILE CRC mismatch")
	errInvalidSearchRange   = errors.New("invalid root filesystem search range")
)

// Extract verifies firmware records and streams the root filesystem.
func Extract(source io.ReaderAt, size int64, output io.Writer) error {
	if err := validateFirmwareHeader(source, size); err != nil {
		return err
	}
	part, err := locateRootfs(source, size)
	if err != nil {
		return err
	}
	start, length, err := rootfsRange(source, part.offset, part.end)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(output, io.NewSectionReader(source, start, length), length); err != nil {
		return fmt.Errorf("extract root filesystem: %w", err)
	}
	return nil
}

func validateFirmwareHeader(source io.ReaderAt, size int64) error {
	if size < ubntHeaderSize {
		return errTruncatedHeader
	}
	if size > maximumFirmwareSize {
		return errFirmwareTooLarge
	}
	header := make([]byte, ubntHeaderSize)
	if _, err := source.ReadAt(header, 0); err != nil {
		return fmt.Errorf("read firmware header: %w", err)
	}
	if string(header[:4]) != "UBNT" {
		return errNotUBNTImage
	}
	if crc32.ChecksumIEEE(header[:ubntHeaderSize-4]) != binary.BigEndian.Uint32(header[ubntHeaderSize-4:]) {
		return errHeaderCRCMismatch
	}
	return nil
}

type rootfsRegion struct {
	offset, end int64
}

func locateRootfs(source io.ReaderAt, size int64) (rootfsRegion, error) {
	offset, fileRoot, err := validateFiles(source, size)
	if err != nil {
		return rootfsRegion{}, err
	}
	part, err := findRootfs(source, offset, size)
	if err != nil {
		return rootfsRegion{}, err
	}
	if part < 0 {
		part, err = findRootfs(source, ubntHeaderSize, size)
		if err != nil {
			return rootfsRegion{}, err
		}
	}
	if part < 0 {
		if fileRoot.end != 0 {
			return fileRoot, nil
		}
		return rootfsRegion{}, errNoRootfs
	}
	return rootfsRegion{part, size}, nil
}

func recordFits(offset, size, need int64) bool {
	return offset >= 0 && size >= 0 && size <= maximumFirmwareSize && offset <= size && size-offset >= need
}

func rootfsRange(source io.ReaderAt, part, size int64) (int64, int64, error) {
	if !recordFits(part, size, fileRecordHeaderSize+squashfsSuperblockSize) {
		return 0, 0, errMissingSuperblock
	}
	start := part + fileRecordHeaderSize
	block := make([]byte, squashfsSuperblockSize)
	if _, err := source.ReadAt(block, start); err != nil {
		return 0, 0, fmt.Errorf("read squashfs superblock: %w", err)
	}
	if string(block[:4]) != "hsqs" {
		return 0, 0, errMissingSuperblock
	}
	length, err := squashfsLength(block, size-start)
	if err != nil {
		return 0, 0, err
	}
	return start, length, nil
}

func squashfsLength(superblock []byte, available int64) (int64, error) {
	length := binary.LittleEndian.Uint64(superblock[40:48])
	if length < squashfsSuperblockSize || length > math.MaxInt64 {
		return 0, errInvalidRootfsLength
	}
	if int64(length) > available {
		return 0, errRootfsExceedsImage
	}
	return int64(length), nil
}

func validateFiles(source io.ReaderAt, size int64) (int64, rootfsRegion, error) {
	offset := int64(ubntHeaderSize)
	var root rootfsRegion
	var tag [4]byte
	for offset+4 <= size {
		if _, err := source.ReadAt(tag[:], offset); err != nil {
			return 0, root, fmt.Errorf("read firmware record tag at %d: %w", offset, err)
		}
		switch string(tag[:]) {
		case "\x00\x00\x00\x00":
			offset += 4
		case "FILE":
			next, err := validateFile(source, offset, size)
			if err != nil {
				return 0, root, err
			}
			candidate, err := fileRootfs(source, offset, next-crcFooterSize)
			if err != nil {
				return 0, root, err
			}
			if candidate {
				root = rootfsRegion{offset, next - crcFooterSize}
			}
			offset = next
		default:
			return offset, root, nil
		}
	}

	return offset, root, nil
}

// Only a validated FILE payload beginning with squashfs is a fallback root.
// Its own payload length bounds bytes_used, even if more firmware follows it.
func fileRootfs(source io.ReaderAt, offset, end int64) (bool, error) {
	var magic [4]byte
	if end-offset-fileRecordHeaderSize < int64(len(magic)) {
		return false, nil
	}
	if _, err := source.ReadAt(magic[:], offset+fileRecordHeaderSize); err != nil {
		return false, fmt.Errorf("read FILE payload magic: %w", err)
	}

	return string(magic[:]) == "hsqs", nil
}

func validateFile(source io.ReaderAt, offset, size int64) (int64, error) {
	if !recordFits(offset, size, fileRecordHeaderSize+crcFooterSize) {
		return 0, errTruncatedFileHeader
	}
	header := make([]byte, fileRecordHeaderSize)
	if _, err := source.ReadAt(header, offset); err != nil {
		return 0, fmt.Errorf("read FILE header at %d: %w", offset, err)
	}
	length := int64(binary.BigEndian.Uint32(header[48:52]))
	if length > size-offset-fileRecordHeaderSize-crcFooterSize {
		return 0, errTruncatedFilePayload
	}
	end := offset + fileRecordHeaderSize + length
	checksum := crc32.NewIEEE()
	if _, err := io.Copy(checksum, io.NewSectionReader(source, offset, end-offset)); err != nil {
		return 0, fmt.Errorf("checksum FILE record at %d: %w", offset, err)
	}
	var footer [4]byte
	if _, err := source.ReadAt(footer[:], end); err != nil {
		return 0, fmt.Errorf("read FILE checksum at %d: %w", end, err)
	}
	if checksum.Sum32() != binary.BigEndian.Uint32(footer[:]) {
		return 0, errFileCRCMismatch
	}

	return end + crcFooterSize, nil
}

func findRootfs(source io.ReaderAt, start, size int64) (int64, error) {
	if start < 0 || size < start || size > maximumFirmwareSize {
		return -1, errInvalidSearchRange
	}
	marker := []byte("PARTrootfs")
	buffer := make([]byte, rootfsScanBufferSize)
	for start < size {
		n, err := source.ReadAt(buffer[:min(int64(len(buffer)), size-start)], start)
		if err != nil && err != io.EOF {
			return 0, fmt.Errorf("scan for root filesystem at %d: %w", start, err)
		}
		if index := bytes.Index(buffer[:n], marker); index >= 0 {
			return start + int64(index), nil
		}
		if n < len(marker) {
			break
		}
		start += int64(n - len(marker) + 1)
	}

	return -1, nil
}
