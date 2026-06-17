package log

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	// Table-driven test: multiple inputs, same logic
	testCases := []struct {
		name string
		data []byte
	}{
		{name: "simple string", data: []byte("hello world")},
		{name: "empty data", data: []byte{}},
		{name: "single byte", data: []byte{0x42}},
		{name: "binary data", data: []byte{0x00, 0xFF, 0x01, 0xFE}},
		{name: "large data", data: bytes.Repeat([]byte("x"), 10000)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Encode
			record := Encode(tc.data)

			// Verify total size
			expectedSize := headerSize + len(tc.data)
			if len(record) != expectedSize {
				t.Fatalf("record size: got %d, want %d", len(record), expectedSize)
			}

			// Decode
			got, err := Decode(record)
			if err != nil {
				t.Fatalf("decode failed: %v", err)
			}

			// Verify data matches
			if !bytes.Equal(got, tc.data) {
				t.Errorf("data mismatch: got %q, want %q", got, tc.data)
			}
		})
	}
}

func TestEncodeFormat(t *testing.T) {
	data := []byte("hello")

	record := Encode(data)

	// Verify length field (first 8 bytes)
	encodedLen := binary.BigEndian.Uint64(record[0:lenSize])
	if encodedLen != 5 {
		t.Errorf("encoded length: got %d, want 5", encodedLen)
	}

	// Verify checksum field (next 4 bytes)
	encodedChecksum := binary.BigEndian.Uint32(record[lenSize:headerSize])
	expectedChecksum := crc32.ChecksumIEEE(data)
	if encodedChecksum != expectedChecksum {
		t.Errorf("encoded checksum: got 0x%08X, want 0x%08X",
			encodedChecksum, expectedChecksum)
	}

	// Verify data bytes
	if !bytes.Equal(record[headerSize:], data) {
		t.Errorf("encoded data: got %q, want %q", record[headerSize:], data)
	}
}

func TestDecodeTooShortForHeader(t *testing.T) {
	// A record that's only 5 bytes — can't even hold the 12-byte header
	shortRecord := []byte{0x00, 0x00, 0x00, 0x00, 0x00}

	_, err := Decode(shortRecord)
	if err == nil {
		t.Fatal("expected error for short record, got nil")
	}

	t.Logf("Got expected error: %v", err)
}

func TestDecodeTruncatedData(t *testing.T) {
	data := []byte("hello world") // 11 bytes

	// Encode a full record, then chop off the last 3 bytes
	// This simulates a crash mid-write: header is intact but data is incomplete
	fullRecord := Encode(data)
	truncated := fullRecord[:len(fullRecord)-3]

	_, err := Decode(truncated)
	if err == nil {
		t.Fatal("expected error for truncated record, got nil")
	}

	t.Logf("Got expected error: %v", err)
}

func TestDecodeChecksumMismatch(t *testing.T) {
	data := []byte("hello world")

	record := Encode(data)

	// Corrupt one byte in the data section (byte 15 is inside "hello world")
	record[headerSize+2] ^= 0xFF // flip all bits in the 3rd data byte

	_, err := Decode(record)
	if err == nil {
		t.Fatal("expected error for corrupted data, got nil")
	}

	t.Logf("Got expected error: %v", err)
}

func TestRecordSize(t *testing.T) {
	testCases := []struct {
		dataLen  int
		wantSize int
	}{
		{dataLen: 0, wantSize: 12},    // header only
		{dataLen: 1, wantSize: 13},    // header + 1 byte
		{dataLen: 100, wantSize: 112}, // header + 100 bytes
		{dataLen: 10000, wantSize: 10012},
	}

	for _, tc := range testCases {
		got := RecordSize(tc.dataLen)
		if got != tc.wantSize {
			t.Errorf("RecordSize(%d): got %d, want %d",
				tc.dataLen, got, tc.wantSize)
		}
	}
}
