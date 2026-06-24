package log

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewLogCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}
}

func TestNewLogCreatesFirstSegment(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	segPath := filepath.Join(dir, "segment-000001.log")
	if _, err := os.Stat(segPath); err != nil {
		t.Fatalf("first segment not created: %v", err)
	}
}

func TestAppendAndReadSingleRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
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
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAppendAndReadMultipleRecords(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	records := []string{"first", "second", "third", "fourth", "fifth"}
	offsets := make([]uint64, len(records))

	var err2 error
	for i, s := range records {
		offsets[i], err2 = l.Append([]byte(s))
		if err2 != nil {
			t.Fatalf("Append(%q) failed: %v", s, err2)
		}
	}

	for i, s := range records {
		got, err := l.Read(offsets[i])
		if err != nil {
			t.Fatalf("Read(%d) failed: %v", offsets[i], err)
		}
		if string(got) != s {
			t.Errorf("Read(%d): got %q, want %q", offsets[i], got, s)
		}
	}
}

func TestReadRandomAccess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
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
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
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
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	_, err = l.Read(0)
	if err == nil {
		t.Fatal("expected error reading from empty log")
	}

	l.Append([]byte("only record"))

	_, err = l.Read(1)
	if err == nil {
		t.Fatal("expected error reading offset 1 with 1 record")
	}

	_, err = l.Read(999)
	if err == nil {
		t.Fatal("expected error reading offset 999")
	}
}

func TestReadLargeRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

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

func TestSegmentRotation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	// Each "record-X" is 8 bytes data → 20 bytes on disk.
	// maxSegSize=50 means 2 records fit (40 bytes), third triggers rotation on next append.
	l, err := NewLog(dir, 50)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	for i := 0; i < 6; i++ {
		_, err := l.Append([]byte(fmt.Sprintf("record-%d", i)))
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	// Count segment files.
	entries, _ := os.ReadDir(dir)
	segCount := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log") {
			segCount++
		}
	}
	if segCount < 2 {
		t.Errorf("expected multiple segments, got %d", segCount)
	}

	t.Logf("created %d segments for 6 records", segCount)

	// All records should be readable across segments.
	for i := 0; i < 6; i++ {
		got, err := l.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d) failed: %v", i, err)
		}
		want := fmt.Sprintf("record-%d", i)
		if string(got) != want {
			t.Errorf("Read(%d): got %q, want %q", i, got, want)
		}
	}
}

func TestSegmentRotationBoundary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	// "ab" is 2 bytes data → 14 bytes per record.
	// maxSegSize=14 means exactly 1 record per segment.
	l, err := NewLog(dir, 14)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	records := []string{"ab", "cd", "ef", "gh"}
	for _, s := range records {
		if _, err := l.Append([]byte(s)); err != nil {
			t.Fatalf("Append(%q) failed: %v", s, err)
		}
	}

	// Should have 4 segment files (one record each).
	entries, _ := os.ReadDir(dir)
	segCount := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log") {
			segCount++
		}
	}
	if segCount != 4 {
		t.Errorf("expected 4 segments, got %d", segCount)
	}

	// All records readable.
	for i, s := range records {
		got, err := l.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d) failed: %v", i, err)
		}
		if string(got) != s {
			t.Errorf("Read(%d): got %q, want %q", i, got, s)
		}
	}
}

func TestReopenAndReadBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	records := []string{"alpha", "bravo", "charlie"}
	for _, s := range records {
		if _, err := l.Append([]byte(s)); err != nil {
			t.Fatalf("Append(%q) failed: %v", s, err)
		}
	}
	l.Close()

	l2, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog reopen failed: %v", err)
	}
	defer l2.Close()

	for i, s := range records {
		got, err := l2.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d) after reopen: %v", i, err)
		}
		if string(got) != s {
			t.Errorf("Read(%d): got %q, want %q", i, got, s)
		}
	}
}

func TestReopenAndAppendMore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	l.Append([]byte("first"))
	l.Append([]byte("second"))
	l.Close()

	l2, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog reopen failed: %v", err)
	}
	l2.Append([]byte("third"))
	l2.Append([]byte("fourth"))
	l2.Close()

	l3, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog final reopen failed: %v", err)
	}
	defer l3.Close()

	expected := []string{"first", "second", "third", "fourth"}
	for i, s := range expected {
		got, err := l3.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d): %v", i, err)
		}
		if string(got) != s {
			t.Errorf("Read(%d): got %q, want %q", i, got, s)
		}
	}
}

func TestReopenWithSegmentRotation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 50)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	for i := 0; i < 10; i++ {
		l.Append([]byte(fmt.Sprintf("record-%02d", i)))
	}
	l.Close()

	l2, err := NewLog(dir, 50)
	if err != nil {
		t.Fatalf("NewLog reopen failed: %v", err)
	}
	defer l2.Close()

	for i := 0; i < 10; i++ {
		got, err := l2.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d) after reopen: %v", i, err)
		}
		want := fmt.Sprintf("record-%02d", i)
		if string(got) != want {
			t.Errorf("Read(%d): got %q, want %q", i, got, want)
		}
	}
}

func TestReopenEmptyLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	l.Close()

	l2, err := NewLog(dir, 0)
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
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	l.Append([]byte("good record one"))
	l.Append([]byte("good record two"))
	l.Append([]byte("this will be corrupted"))
	l.Close()

	// Corrupt the last record.
	// Record 0: 12 + 15 = 27 bytes, starts at byte 0
	// Record 1: 12 + 15 = 27 bytes, starts at byte 27
	// Record 2: 12 + 22 = 34 bytes, starts at byte 54
	// Corrupt byte 54 + 12 + 2 = byte 68 (inside record 2's data).
	segPath := filepath.Join(dir, "segment-000001.log")
	raw, err := os.OpenFile(segPath, os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open raw file: %v", err)
	}
	corruptPos := int64(54 + headerSize + 2)
	oneByte := make([]byte, 1)
	raw.ReadAt(oneByte, corruptPos)
	oneByte[0] ^= 0xFF
	raw.WriteAt(oneByte, corruptPos)
	raw.Close()

	l2, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog after corruption: %v", err)
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

	// Record 2 should not be readable.
	_, err = l2.Read(2)
	if err == nil {
		t.Fatal("expected error reading corrupted record 2")
	}
}

func TestNewLogInitialSizeEmptyFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	if l.Size() != 0 {
		t.Fatalf("initial size: got %d, want 0", l.Size())
	}
}

func TestNewLogInitialSizeExistingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	// Write one record using a proper log session.
	l, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	l.Append([]byte("hello world"))
	l.Close()

	// Reopen and check size.
	l2, err := NewLog(dir, 0)
	if err != nil {
		t.Fatalf("NewLog reopen failed: %v", err)
	}
	defer l2.Close()

	expectedSize := int64(RecordSize(len("hello world")))
	if l2.Size() != expectedSize {
		t.Fatalf("size after reopen: got %d, want %d", l2.Size(), expectedSize)
	}
}
