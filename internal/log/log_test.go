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

	l.Append([]byte("important data"))
	l.Close()

	// Corrupt the data section.
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

	// Reopen. buildIndex should find zero valid records.
	l2, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog after corruption: %v", err)
	}
	defer l2.Close()

	_, err = l2.Read(0)
	if err == nil {
		t.Fatal("expected error reading corrupted record, got nil")
	}
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

func TestReopenAndReadBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// Open, write some records, close.
	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	records := []string{"alpha", "bravo", "charlie"}
	for _, s := range records {
		if _, err := l.Append([]byte(s)); err != nil {
			t.Fatalf("Append(%q) failed: %v", s, err)
		}
	}

	if err := l.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen the same file.
	l2, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog reopen failed: %v", err)
	}
	defer l2.Close()

	// All records should be readable without manually setting up the index.
	for i, s := range records {
		got, err := l2.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d) after reopen failed: %v", i, err)
		}
		if string(got) != s {
			t.Errorf("Read(%d) after reopen: got %q, want %q", i, got, s)
		}
	}
}

func TestReopenAndAppendMore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// First session: write 2 records.
	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	l.Append([]byte("first"))
	l.Append([]byte("second"))
	l.Close()

	// Second session: reopen, write 2 more.
	l2, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog reopen failed: %v", err)
	}

	l2.Append([]byte("third"))
	l2.Append([]byte("fourth"))
	l2.Close()

	// Third session: reopen, verify all 4.
	l3, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog final reopen failed: %v", err)
	}
	defer l3.Close()

	expected := []string{"first", "second", "third", "fourth"}
	for i, s := range expected {
		got, err := l3.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d) failed: %v", i, err)
		}
		if string(got) != s {
			t.Errorf("Read(%d): got %q, want %q", i, got, s)
		}
	}
}

func TestReopenEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// Create and close an empty log.
	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	l.Close()

	// Reopen. Should have zero records.
	l2, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog reopen failed: %v", err)
	}
	defer l2.Close()

	if l2.Size() != 0 {
		t.Errorf("size after reopen empty: got %d, want 0", l2.Size())
	}

	_, err = l2.Read(0)
	if err == nil {
		t.Fatal("expected error reading from empty reopened log")
	}
}

func TestReopenDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// Write 3 records.
	l, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	l.Append([]byte("good record one"))
	l.Append([]byte("good record two"))
	l.Append([]byte("this will be corrupted"))
	l.Close()

	// Corrupt the last record: flip a byte in its data section.
	// First, we need to know where the last record starts.
	// Record 0: headerSize + 15 = 27 bytes, starts at 0
	// Record 1: headerSize + 15 = 27 bytes, starts at 27
	// Record 2: headerSize + 22 = 34 bytes, starts at 54
	// Corrupt a byte in record 2's data: byte 54 + headerSize + 2 = byte 68
	raw, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open raw file: %v", err)
	}
	corruptPos := int64(54 + headerSize + 2)
	oneByte := make([]byte, 1)
	raw.ReadAt(oneByte, corruptPos)
	oneByte[0] ^= 0xFF
	raw.WriteAt(oneByte, corruptPos)
	raw.Close()

	// Reopen. buildIndex should find records 0 and 1, stop at corrupted record 2.
	l2, err := NewLog(path)
	if err != nil {
		t.Fatalf("NewLog after corruption failed: %v", err)
	}
	defer l2.Close()

	// Records 0 and 1 should be readable.
	got, err := l2.Read(0)
	if err != nil {
		t.Fatalf("Read(0) failed: %v", err)
	}
	if string(got) != "good record one" {
		t.Errorf("Read(0): got %q, want %q", got, "good record one")
	}

	got, err = l2.Read(1)
	if err != nil {
		t.Fatalf("Read(1) failed: %v", err)
	}
	if string(got) != "good record two" {
		t.Errorf("Read(1): got %q, want %q", got, "good record two")
	}

	// Record 2 should not exist in the index.
	_, err = l2.Read(2)
	if err == nil {
		t.Fatal("expected error reading corrupted record 2, got nil")
	}
}
