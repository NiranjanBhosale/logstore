// Package log implements a single-node append-only log store with
// configurable durability and crash recovery.
//
// Records are stored in segment files with a fixed 12-byte header
// containing the data length and a CRC32 checksum. The log supports
// three sync modes: no sync, per-write fsync, and batched fsync.
package log

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

const (
	// lenSize is the number of bytes used to store the record data length.
	// uint64 = 8 bytes, supports data up to ~18.4 exabytes.
	lenSize = 8

	// crcSize is the number of bytes used to store the CRC32 checksum.
	// uint32 = 4 bytes.
	crcSize = 4

	// headerSize is the total fixed overhead per record.
	// Every record on disk starts with exactly these many bytes
	// before the actual data begins.
	headerSize = lenSize + crcSize // 12 bytes
)

// Encode converts raw data into the on-disk record format.
// The returned byte slice contains the 12-byte header followed by the data,
// ready to be written directly to a segment file.
func Encode(data []byte) []byte {

	dataLen := uint64(len(data))
	checksum := crc32.ChecksumIEEE(data)

	record := make([]byte, headerSize+uint64(len(data)))

	// Write the length into bytes 0-7
	binary.BigEndian.PutUint64(record[0:lenSize], dataLen)

	// Write the checksum into bytes 8-11
	binary.BigEndian.PutUint32(record[lenSize:headerSize], checksum)

	// Copy the data into bytes 12 onward
	copy(record[headerSize:], data)

	return record

}

// Decode extracts the data from a record byte slice and validates its CRC32 checksum.
// It returns the data and any error encountered.
//
// Possible errors:
//   - record shorter than the 12-byte header
//   - record shorter than header claims (truncated data)
//   - checksum mismatch (corrupted data)
func Decode(record []byte) ([]byte, error) {

	// Check 1: Confirm we have enough data
	if len(record) < headerSize {
		return nil, fmt.Errorf("record too short for header: got %d bytes, need %d",
			len(record), headerSize)
	}

	// Extract header fields
	dataLen := binary.BigEndian.Uint64(record[0:lenSize])
	storedChecksum := binary.BigEndian.Uint32(record[lenSize:headerSize])

	// Check 2: Confirm data is available
	totalNeeded := headerSize + int(dataLen)
	if len(record) < totalNeeded {
		return nil, fmt.Errorf("record truncated: header says %d data bytes, but only %d available",
			dataLen, len(record)-headerSize)
	}

	data := record[headerSize:]

	// Check 3: Verify data integrity
	actualChecksum := crc32.ChecksumIEEE(data)
	if storedChecksum != actualChecksum {
		return nil, fmt.Errorf("checksum mismatch: stored 0x%08X, computed 0x%08X",
			storedChecksum, actualChecksum)
	}

	return data, nil

}

// RecordSize returns the total number of bytes a record with the given
// data length will occupy on disk (header + data).
func RecordSize(dataLen int) int {
	return headerSize + dataLen
}
