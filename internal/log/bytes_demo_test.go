package log

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"testing"
)

func TestHowNumbersAreStored(t *testing.T) {
	buf := make([]byte, 8)

	// Store the number 11 in big-endian format
	binary.BigEndian.PutUint64(buf, 11)

	// Print each byte
	fmt.Println("Number 11 stored in 8 bytes (big-endian):")
	for i, b := range buf {
		fmt.Printf("  byte[%d] = 0x%02X  (decimal %d)\n", i, b, b)
	}
	// Output:
	// byte[0] = 0x00  (decimal 0)
	// byte[1] = 0x00  (decimal 0)
	// byte[2] = 0x00  (decimal 0)
	// byte[3] = 0x00  (decimal 0)
	// byte[4] = 0x00  (decimal 0)
	// byte[5] = 0x00  (decimal 0)
	// byte[6] = 0x00  (decimal 0)
	// byte[7] = 0x0B  (decimal 11)

	// Now let's try a bigger number: 100,000
	binary.BigEndian.PutUint64(buf, 100_000)
	fmt.Println("\nNumber 100,000 stored in 8 bytes (big-endian):")
	for i, b := range buf {
		fmt.Printf("  byte[%d] = 0x%02X  (decimal %d)\n", i, b, b)
	}
	// Output:
	// byte[0] = 0x00  (decimal 0)
	// byte[1] = 0x00  (decimal 0)
	// byte[2] = 0x00  (decimal 0)
	// byte[3] = 0x00  (decimal 0)
	// byte[4] = 0x00  (decimal 0)
	// byte[5] = 0x01  (decimal 1)
	// byte[6] = 0x86  (decimal 134)
	// byte[7] = 0xA0  (decimal 160)
	// Because 100000 = 1*65536 + 134*256 + 160 = 0x000186A0

	// Now read it back
	readBack := binary.BigEndian.Uint64(buf)
	if readBack != 100_000 {
		t.Errorf("got %d, want 100000", readBack)
	}
}

func TestRecordLayout(t *testing.T) {
	data := []byte("hello from the log store")
	checksum := crc32.ChecksumIEEE(data)

	header := make([]byte, 12)
	binary.BigEndian.PutUint64(header[0:8], uint64(len(data)))
	binary.BigEndian.PutUint32(header[8:12], checksum)

	var buf bytes.Buffer
	buf.Write(header)
	buf.Write(data)
	record := buf.Bytes()

	fmt.Printf("Total record size: %d bytes\n", len(record))
	fmt.Printf("Data length: %d\n", len(data))
	fmt.Printf("CRC32 checksum: 0x%08X\n", checksum)
	fmt.Println()

	// Print the header bytes
	fmt.Println("Header (12 bytes):")
	fmt.Printf("  Length field (bytes 0-7):   ")
	for _, b := range record[0:8] {
		fmt.Printf("0x%02X ", b)
	}
	fmt.Println()
	fmt.Printf("  Checksum field (bytes 8-11): ")
	for _, b := range record[8:12] {
		fmt.Printf("0x%02X ", b)
	}
	fmt.Println()

	// Print the data bytes
	fmt.Println("Data (bytes 12-35):")
	fmt.Printf("  As hex:    ")
	for _, b := range record[12:] {
		fmt.Printf("0x%02X ", b)
	}
	fmt.Println()
	fmt.Printf("  As string: %s\n", string(record[12:]))

	// The exact same layout we'll write to disk files
	// Reading is: read 12 bytes header, parse length & checksum,
	//             read 'length' more bytes, verify checksum
}
