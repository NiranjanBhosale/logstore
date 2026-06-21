package log

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sync"
)

// Log is a single-segment append-only log stored in one file.
type Log struct {
	mu sync.Mutex

	file   *os.File
	writer *bufio.Writer

	// index maps logical record offsets to byte positions in the file.
	index []int64

	// size tracks how many bytes are currently stored in the file.
	// This can also be called as the last byte tracked
	size int64
}

// NewLog opens or creates a single-file append-only log at path.
func NewLog(path string) (*Log, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", path, err)
	}

	l := &Log{
		file:   file,
		writer: bufio.NewWriter(file),
		index:  make([]int64, 0),
	}

	if err := l.buildIndex(); err != nil {
		file.Close()
		return nil, fmt.Errorf("build index for %q: %w", path, err)
	}

	// Seek to the end so the bufio.Writer appends after existing data.
	if _, err := l.file.Seek(l.size, io.SeekStart); err != nil {
		file.Close()
		return nil, fmt.Errorf("seek to end: %w", err)
	}

	return l, nil
}

// Size returns the current number of bytes in the log file.
func (l *Log) Size() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.size
}

// Close flushes buffered data and closes the underlying file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.writer != nil {
		if err := l.writer.Flush(); err != nil {
			return fmt.Errorf("flush writer: %w", err)
		}
	}

	if l.file != nil {
		if err := l.file.Close(); err != nil {
			return fmt.Errorf("close file: %w", err)
		}
	}

	return nil
}

// Append writes data to the log and returns the offset assigned to this record.
func (l *Log) Append(data []byte) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// The offset for this record is the current number of records.
	// If we have 0 records, this new one gets offset 0.
	// If we have 5 records, this new one gets offset 5.
	offset := uint64(len(l.index))

	record := Encode(data)
	bytePos := l.size

	n, err := l.writer.Write(record)
	if err != nil {
		return 0, fmt.Errorf("append record: %w", err)
	}

	l.index = append(l.index, bytePos)
	l.size += int64(n)

	return offset, nil
}

func (l *Log) Read(offset uint64) ([]byte, error) {

	l.mu.Lock()
	defer l.mu.Unlock()

	// Check if this offset exists.
	if offset >= uint64(len(l.index)) {
		return nil, fmt.Errorf("offset %d out of range: log has %d records",
			offset, len(l.index))
	}

	if err := l.writer.Flush(); err != nil {
		return nil, fmt.Errorf("flush before read: %w", err)
	}

	bytePos := l.index[offset]

	if _, err := l.file.Seek(bytePos, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek to offset %d (byte %d): %w",
			offset, bytePos, err)
	}

	// Figure out how many bytes this record occupies.
	var size int64
	if int(offset+1) < len(l.index) {
		// Not the last record: ends where the next record begins.
		size = l.index[offset+1] - bytePos
	} else {
		// Last record: ends at EOF.
		size = l.size - bytePos
	}

	// Read the raw bytes.
	record := make([]byte, size)
	if _, err := io.ReadFull(l.file, record); err != nil {
		return nil, fmt.Errorf("read record at offset %d: %w", offset, err)
	}

	// Decode handles header parsing, checksum validation, everything.
	return Decode(record)

}

func (l *Log) buildIndex() error {
	if _, err := l.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek to start: %w", err)
	}

	var pos int64

	for {

		header := make([]byte, headerSize)
		_, err := io.ReadFull(l.file, header)

		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}

		if err != nil {
			return fmt.Errorf("read header at byte %d: %w", pos, err)
		}

		dataLen := binary.BigEndian.Uint64(header[0:lenSize])
		data := make([]byte, dataLen)

		_, err = io.ReadFull(l.file, data)

		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}

		if err != nil {
			return fmt.Errorf("read data at byte %d: %w", pos, err)
		}

		storedChecksum := binary.BigEndian.Uint32(header[lenSize:headerSize])
		newChecksum := crc32.ChecksumIEEE(data)

		if storedChecksum != newChecksum {
			break
		}

		l.index = append(l.index, pos)
		pos += int64(headerSize + dataLen)
	}

	l.size = pos
	return nil
}
