package log

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestNewLogCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat created file: %v", err)
	}

	if info.IsDir() {
		t.Fatalf("expected a file, got directory")
	}
}

func TestNewLogInitialSizeEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	if l.size != 0 {
		t.Fatalf("initial size: got %d, want 0", l.size)
	}

	if len(l.index) != 0 {
		t.Fatalf("initial index length: got %d, want 0", len(l.index))
	}
}

func TestNewLogInitialSizeExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// Pre-create a file with known contents.
	initialData := []byte("hello world")
	if err := os.WriteFile(path, initialData, 0644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	if l.size != int64(len(initialData)) {
		t.Fatalf("initial size: got %d, want %d", l.size, len(initialData))
	}
}

func TestClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	if err := l.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestAppendSingleRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	data := []byte("hello world")
	offset, err := l.Append(data)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// First record should get offset 0
	if offset != 0 {
		t.Errorf("offset: got %d, want 0", offset)
	}

	// Size should be header (12 bytes) + data (11 bytes) = 23 bytes
	expectedSize := int64(RecordSize(len(data)))
	if l.Size() != expectedSize {
		t.Errorf("size: got %d, want %d", l.Size(), expectedSize)
	}
}

func TestAppendMultipleRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	records := []string{"first", "second", "third"}

	for i, s := range records {
		offset, err := l.Append([]byte(s))
		if err != nil {
			t.Fatalf("Append(%q) failed: %v", s, err)
		}
		if offset != uint64(i) {
			t.Errorf("Append(%q): offset got %d, want %d", s, offset, i)
		}
	}

	// Calculate expected total size
	expectedSize := int64(0)
	for _, s := range records {
		expectedSize += int64(RecordSize(len(s)))
	}

	if l.Size() != expectedSize {
		t.Errorf("total size: got %d, want %d", l.Size(), expectedSize)
	}
}

func TestAppendAndReadSingleRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	want := []byte("hello world")
	offset, err := l.Append(want)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	got, err := l.Read(offset)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("Read returned %q, want %q", got, want)
	}
}

func TestAppendAndReadMultipleRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	records := []string{
		"first record",
		"a",
		"this is a longer third record with more data",
		"",
		"fifth",
	}

	offsets := make([]uint64, len(records))
	for i, s := range records {
		offsets[i], err = l.Append([]byte(s))
		if err != nil {
			t.Fatalf("Append(%q) failed: %v", s, err)
		}
	}

	for i, s := range records {
		got, err := l.Read(offsets[i])
		if err != nil {
			t.Fatalf("Read(offset %d) failed: %v", offsets[i], err)
		}
		if string(got) != s {
			t.Errorf("Read(offset %d): got %q, want %q", offsets[i], got, s)
		}
	}
}

func TestReadRandomAccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	records := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	for _, s := range records {
		if _, err := l.Append([]byte(s)); err != nil {
			t.Fatalf("Append(%q) failed: %v", s, err)
		}
	}

	// Read in arbitrary order: 3, 0, 4, 1, 2
	order := []int{3, 0, 4, 1, 2}
	for _, i := range order {
		got, err := l.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d) failed: %v", i, err)
		}
		if string(got) != records[i] {
			t.Errorf("Read(%d): got %q, want %q", i, got, records[i])
		}
	}
}

func TestReadEmptyData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	offset, err := l.Append([]byte{})
	if err != nil {
		t.Fatalf("Append empty failed: %v", err)
	}

	got, err := l.Read(offset)
	if err != nil {
		t.Fatalf("Read empty failed: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("expected empty data, got %q", got)
	}
}

func TestReadInvalidOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	// Empty log, offset 0 should fail.
	_, err = l.Read(0)
	if err == nil {
		t.Fatal("expected error reading from empty log, got nil")
	}

	// Add one record, offset 1 should fail.
	l.Append([]byte("only record"))

	_, err = l.Read(1)
	if err == nil {
		t.Fatal("expected error reading offset 1 with only 1 record, got nil")
	}

	// Large offset should fail.
	_, err = l.Read(999)
	if err == nil {
		t.Fatal("expected error reading offset 999, got nil")
	}
}

func TestReadDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	_, err = l.Append([]byte("important data"))
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	l.Close()

	// Corrupt the file: flip a byte in the data section.
	raw, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open raw file: %v", err)
	}
	corruptPos := int64(headerSize + 2)
	oneByte := make([]byte, 1)
	raw.ReadAt(oneByte, corruptPos)
	oneByte[0] ^= 0xFF
	raw.WriteAt(oneByte, corruptPos)
	raw.Close()

	// Reopen and try to read.
	l2, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog after corruption: %v", err)
	}
	defer l2.Close()

	// Manually rebuild the index (crash recovery will automate this in Week 2).
	l2.index = append(l2.index, 0)

	_, err = l2.Read(0)
	if err == nil {
		t.Fatal("expected checksum error, got nil")
	}

	t.Logf("Got expected error: %v", err)
}

func TestReadLargeRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	// 1MB of data
	want := bytes.Repeat([]byte("x"), 1024*1024)
	offset, err := l.Append(want)
	if err != nil {
		t.Fatalf("Append large record failed: %v", err)
	}

	got, err := l.Read(offset)
	if err != nil {
		t.Fatalf("Read large record failed: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("large record: got %d bytes, want %d bytes", len(got), len(want))
	}
}
