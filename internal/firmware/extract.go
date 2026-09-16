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

// Extract verifies firmware records and streams the root filesystem.
func Extract(source io.ReaderAt, size int64, output io.Writer) error {
	if size < ubntHeaderSize {
		return errors.New("truncated firmware header")
	}
	if size > maximumFirmwareSize {
		return errors.New("firmware exceeds four GiB")
	}
	header := make([]byte, ubntHeaderSize)
	if _, err := source.ReadAt(header, 0); err != nil {
		return fmt.Errorf("read firmware header: %w", err)
	}
	if string(header[:4]) != "UBNT" {
		return errors.New("not a UBNT firmware image")
	}
	if crc32.ChecksumIEEE(header[:ubntHeaderSize-4]) != binary.BigEndian.Uint32(header[ubntHeaderSize-4:]) {
		return errors.New("firmware header CRC mismatch")
	}
	offset, err := validateFiles(source, size)
	if err != nil {
		return err
	}
	part, err := findRootfs(source, offset, size)
	if err != nil {
		return err
	}
	if part < 0 {
		part, err = findRootfs(source, ubntHeaderSize, size)
	}
	if err != nil {
		return err
	}
	if part < 0 {
		return errors.New("no PARTrootfs found")
	}
	start, length, err := rootfsRange(source, part, size)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(output, io.NewSectionReader(source, start, length), length); err != nil {
		return fmt.Errorf("extract root filesystem: %w", err)
	}
	return nil
}

func rootfsRange(source io.ReaderAt, part, size int64) (int64, int64, error) {
	if part < 0 || size < 0 || size > maximumFirmwareSize || part > size || size-part < fileRecordHeaderSize+squashfsSuperblockSize {
		return 0, 0, errors.New("missing squashfs superblock")
	}
	start := part + fileRecordHeaderSize
	block := make([]byte, squashfsSuperblockSize)
	if _, err := source.ReadAt(block, start); err != nil {
		return 0, 0, fmt.Errorf("read squashfs superblock: %w", err)
	}
	if string(block[:4]) != "hsqs" {
		return 0, 0, errors.New("missing squashfs superblock")
	}
	length := binary.LittleEndian.Uint64(block[40:48])
	if length < squashfsSuperblockSize || length > math.MaxInt64 {
		return 0, 0, errors.New("invalid squashfs length")
	}
	if int64(length) > size-start {
		return 0, 0, errors.New("squashfs exceeds firmware size")
	}
	return start, int64(length), nil
}

func validateFiles(source io.ReaderAt, size int64) (int64, error) {
	offset := int64(ubntHeaderSize)
	var tag [4]byte
	for offset+4 <= size {
		if _, err := source.ReadAt(tag[:], offset); err != nil {
			return 0, err
		}
		switch string(tag[:]) {
		case "\x00\x00\x00\x00":
			offset += 4
		case "FILE":
			next, err := validateFile(source, offset, size)
			if err != nil {
				return 0, err
			}
			offset = next
		default:
			return offset, nil
		}
	}

	return offset, nil
}

func validateFile(source io.ReaderAt, offset, size int64) (int64, error) {
	header := make([]byte, fileRecordHeaderSize)
	if offset < 0 || size < 0 || size > maximumFirmwareSize || offset > size || size-offset < int64(len(header))+crcFooterSize {
		return 0, errors.New("truncated FILE header")
	}
	if _, err := source.ReadAt(header, offset); err != nil {
		return 0, err
	}
	length := int64(binary.BigEndian.Uint32(header[48:52]))
	if length > size-offset-fileRecordHeaderSize-crcFooterSize {
		return 0, errors.New("truncated FILE payload")
	}
	end := offset + fileRecordHeaderSize + length
	checksum := crc32.NewIEEE()
	if _, err := io.Copy(checksum, io.NewSectionReader(source, offset, end-offset)); err != nil {
		return 0, err
	}
	var footer [4]byte
	if _, err := source.ReadAt(footer[:], end); err != nil {
		return 0, err
	}
	if checksum.Sum32() != binary.BigEndian.Uint32(footer[:]) {
		return 0, errors.New("FILE CRC mismatch")
	}

	return end + crcFooterSize, nil
}

func findRootfs(source io.ReaderAt, start, size int64) (int64, error) {
	if start < 0 || size < start || size > maximumFirmwareSize {
		return -1, errors.New("invalid root filesystem search range")
	}
	marker := []byte("PARTrootfs")
	buffer := make([]byte, rootfsScanBufferSize)
	for start < size {
		n, err := source.ReadAt(buffer[:min(int64(len(buffer)), size-start)], start)
		if err != nil && err != io.EOF {
			return 0, err
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
