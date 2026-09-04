package log

import (
	"bytes"
	"encoding/binary"
	"fmt"
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
			got, n, err := Decode(record)
			if err != nil {
				t.Fatalf("decode failed: %v", err)
			}

			// Verify data matches
			if !bytes.Equal(got, tc.data) {
				t.Errorf("data mismatch: got %q, want %q", got, tc.data)
			}

			// Decode must report the whole record, header included
			if n != expectedSize {
				t.Errorf("consumed bytes: got %d, want %d", n, expectedSize)
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

	_, _, err := Decode(shortRecord)
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

	_, _, err := Decode(truncated)
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

	_, _, err := Decode(record)
	if err == nil {
		t.Fatal("expected error for corrupted data, got nil")
	}

	t.Logf("Got expected error: %v", err)
}

// TestDecodeIgnoresTrailingBytes covers the case Decode used to get wrong.
// It sliced buf[headerSize:] rather than bounding the data by the length the
// header declared, so any extra byte in the buffer was folded into the record
// and the checksum failed, reporting corruption that had not happened.
func TestDecodeIgnoresTrailingBytes(t *testing.T) {
	data := []byte("hello world")
	record := Encode(data)

	padded := make([]byte, 0, len(record)+3)
	padded = append(padded, record...)
	padded = append(padded, 0x00, 0xFF, 0x7F)

	got, n, err := Decode(padded)
	if err != nil {
		t.Fatalf("Decode with trailing bytes failed: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("data: got %q, want %q", got, data)
	}
	if n != len(record) {
		t.Errorf("consumed bytes: got %d, want %d", n, len(record))
	}
}

// TestDecodeBatch is the shape a replication follower needs: several records
// arrive concatenated in one buffer and are walked one at a time, each step
// advancing by the byte count Decode reports.
func TestDecodeBatch(t *testing.T) {
	want := [][]byte{
		[]byte("alpha"),
		{},
		[]byte("charlie"),
		bytes.Repeat([]byte("d"), 5000),
	}

	var wire []byte
	for _, w := range want {
		wire = append(wire, Encode(w)...)
	}

	var got [][]byte
	buf := wire
	for len(buf) > 0 {
		data, n, err := Decode(buf)
		if err != nil {
			t.Fatalf("Decode at %d bytes remaining: %v", len(buf), err)
		}
		// Copy: data aliases wire, which the caller may reuse.
		got = append(got, append([]byte(nil), data...))
		buf = buf[n:]
	}

	if len(got) != len(want) {
		t.Fatalf("decoded %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("record %d: got %d bytes, want %d bytes",
				i, len(got[i]), len(want[i]))
		}
	}
}

// TestDecodeRejectsImpossibleLength checks the bounds check against a length
// field that is corrupt rather than merely wrong. Comparing as int would wrap
// these negative and let the check pass, then panic on the slice.
func TestDecodeRejectsImpossibleLength(t *testing.T) {
	lengths := []uint64{
		1 << 62,
		^uint64(0),       // all bits set
		^uint64(0) - 100, // wraps to a small negative int
		1 << 40,
	}

	for _, dataLen := range lengths {
		t.Run(fmt.Sprintf("len_%d", dataLen), func(t *testing.T) {
			buf := make([]byte, headerSize+4)
			binary.BigEndian.PutUint64(buf[0:lenSize], dataLen)
			binary.BigEndian.PutUint32(buf[lenSize:headerSize], 0)

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Decode panicked on length %d: %v", dataLen, r)
				}
			}()

			_, n, err := Decode(buf)
			if err == nil {
				t.Errorf("Decode accepted an impossible length of %d", dataLen)
			}
			if n != 0 {
				t.Errorf("consumed bytes on error: got %d, want 0", n)
			}
		})
	}
}

// TestDecodeReportsZeroConsumedOnError pins the contract that a caller walking
// a batch cannot advance past a record Decode rejected.
func TestDecodeReportsZeroConsumedOnError(t *testing.T) {
	record := Encode([]byte("hello world"))

	cases := map[string][]byte{
		"short header":      record[:5],
		"truncated data":    record[:len(record)-3],
		"checksum mismatch": append(append([]byte(nil), record[:headerSize+2]...), append([]byte{record[headerSize+2] ^ 0xFF}, record[headerSize+3:]...)...),
	}

	for name, buf := range cases {
		t.Run(name, func(t *testing.T) {
			data, n, err := Decode(buf)
			if err == nil {
				t.Fatal("expected an error")
			}
			if n != 0 {
				t.Errorf("consumed bytes: got %d, want 0", n)
			}
			if data != nil {
				t.Errorf("data: got %q, want nil", data)
			}
			t.Logf("%v", err)
		})
	}
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
