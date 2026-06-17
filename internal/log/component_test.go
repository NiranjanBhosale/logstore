package log

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

// TestBinaryEncoding verifies you understand how we'll encode record headers.
// A record header is: [8 bytes length][4 bytes CRC32]
// Total: 12 bytes, always fixed size.
func TestBinaryEncoding(t *testing.T) {
	data := []byte("hello from the log store")

	// Step 1: compute checksum of the data
	checksum := crc32.ChecksumIEEE(data)

	// Step 2: build the header
	// We need exactly 12 bytes: 8 for uint64 length + 4 for uint32 checksum
	header := make([]byte, 12)
	binary.BigEndian.PutUint64(header[0:8], uint64(len(data)))
	binary.BigEndian.PutUint32(header[8:12], checksum)

	// Step 3: combine header + data into one record
	var buf bytes.Buffer
	buf.Write(header)
	buf.Write(data)
	record := buf.Bytes()

	// Verify total length
	expectedLen := 12 + len(data)
	if len(record) != expectedLen {
		t.Errorf("record length: got %d, want %d", len(record), expectedLen)
	}

	// Step 4: decode — simulate what Read() will do
	// Read the length from the first 8 bytes
	decodedLen := binary.BigEndian.Uint64(record[0:8])
	if decodedLen != uint64(len(data)) {
		t.Errorf("decoded length: got %d, want %d", decodedLen, len(data))
	}

	// Read the checksum from bytes 8-12
	decodedChecksum := binary.BigEndian.Uint32(record[8:12])

	// Read the data starting at byte 12
	decodedData := record[12 : 12+decodedLen]

	// Validate the checksum
	recomputedChecksum := crc32.ChecksumIEEE(decodedData)
	if decodedChecksum != recomputedChecksum {
		t.Errorf("checksum mismatch: stored %d, recomputed %d",
			decodedChecksum, recomputedChecksum)
	}

	// Verify the data matches
	if !bytes.Equal(decodedData, data) {
		t.Errorf("data mismatch: got %q, want %q", decodedData, data)
	}
}

// TestChecksumDetectsCorruption verifies that CRC32 catches bit flips.
// This is exactly what our crash recovery will rely on.
func TestChecksumDetectsCorruption(t *testing.T) {
	data := []byte("important log record")
	checksum := crc32.ChecksumIEEE(data)

	// Corrupt the data (flip one byte)
	corrupted := make([]byte, len(data))
	copy(corrupted, data)
	corrupted[5] ^= 0xFF // XOR with 0xFF flips all bits in that byte

	// The checksum should no longer match
	corruptedChecksum := crc32.ChecksumIEEE(corrupted)
	if checksum == corruptedChecksum {
		// This would be an extraordinarily unlucky CRC collision
		t.Error("checksum matched corrupted data — this should not happen")
	}
}
