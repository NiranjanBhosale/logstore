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

// Decode extracts the data from the record at the start of buf and validates
// its CRC32 checksum. It returns the data, the total number of bytes that
// record occupied in buf, and any error encountered.
//
// buf may be longer than the record. It may hold several records back to back,
// or trailing bytes belonging to no record at all, as a buffer read off a
// network connection generally does. Decode reads only the record at the front
// and reports its size, so a caller holding a batch can walk it:
//
//	for len(buf) > 0 {
//		data, n, err := Decode(buf)
//		if err != nil {
//			return err
//		}
//		use(data)
//		buf = buf[n:]
//	}
//
// Possible errors:
//   - buf shorter than the 12-byte header
//   - buf shorter than the header claims (truncated data)
//   - checksum mismatch (corrupted data)
func Decode(buf []byte) ([]byte, int, error) {

	// Check 1: Confirm we have enough data
	if len(buf) < headerSize {
		return nil, 0, fmt.Errorf("record too short for header: got %d bytes, need %d",
			len(buf), headerSize)
	}

	// Extract header fields
	dataLen := binary.BigEndian.Uint64(buf[0:lenSize])
	storedChecksum := binary.BigEndian.Uint32(buf[lenSize:headerSize])

	// Check 2: Confirm the data is available.
	//
	// dataLen comes from bytes that may be corrupt, so the comparison stays in
	// uint64. Converting first, as int(dataLen), wraps negative for a length
	// near the top of the range and would let the bounds check pass.
	available := uint64(len(buf) - headerSize)
	if dataLen > available {
		return nil, 0, fmt.Errorf("record truncated: header says %d data bytes, but only %d available",
			dataLen, available)
	}

	// Bound the slice by the length the header declared. Taking buf[headerSize:]
	// instead would fold any following record, or any trailing padding, into
	// this record's data. The checksum would then fail and report corruption,
	// pointing at the disk when the real fault is the caller's slice.
	total := headerSize + int(dataLen)
	data := buf[headerSize:total]

	// Check 3: Verify data integrity
	actualChecksum := crc32.ChecksumIEEE(data)
	if storedChecksum != actualChecksum {
		return nil, 0, fmt.Errorf("checksum mismatch: stored 0x%08X, computed 0x%08X",
			storedChecksum, actualChecksum)
	}

	return data, total, nil

}

// RecordSize returns the total number of bytes a record with the given
// data length will occupy on disk (header + data).
func RecordSize(dataLen int) int {
	return headerSize + dataLen
}
