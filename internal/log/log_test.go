package log

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewLogCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

// TestReadThenAppendDoesNotCorrupt is a regression test.
//
// Read used to Seek the segment's file handle, which shares its cursor with
// the bufio.Writer that Append writes through. A read left the cursor
// mid-file, so the next flush overwrote records that were already committed.
// No concurrency was needed to trigger it: a single goroutine appending,
// reading once, then appending again was enough to lose data.
func TestReadThenAppendDoesNotCorrupt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	for i := 0; i < 5; i++ {
		if _, err := l.Append([]byte(fmt.Sprintf("record-%d", i))); err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	// The poison: a read in the middle of the write sequence.
	if _, err := l.Read(0); err != nil {
		t.Fatalf("Read(0) failed: %v", err)
	}

	for i := 5; i < 10; i++ {
		if _, err := l.Append([]byte(fmt.Sprintf("record-%d", i))); err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}
	l.Close()

	// The segment must hold all 10 records. Before the fix it held 6.
	wantSize := int64(10 * RecordSize(len("record-0")))
	info, err := os.Stat(filepath.Join(dir, "segment-000001.log"))
	if err != nil {
		t.Fatalf("stat segment: %v", err)
	}
	if info.Size() != wantSize {
		t.Errorf("segment size: got %d, want %d", info.Size(), wantSize)
	}

	l2, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog reopen failed: %v", err)
	}
	defer l2.Close()

	for i := 0; i < 10; i++ {
		got, err := l2.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d) after reopen: %v", i, err)
		}
		want := fmt.Sprintf("record-%d", i)
		if string(got) != want {
			t.Errorf("Read(%d): got %q, want %q", i, got, want)
		}
	}
}

// TestInterleavedReadAppendAcrossSegments exercises the same hazard while
// segment rotation is happening, so reads land on both the current segment
// and older, fully written ones.
func TestInterleavedReadAppendAcrossSegments(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	// "record-NN" is 9 bytes -> 21 bytes on disk. 50 byte segments hold 2.
	l, err := NewLog(dir, 50, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	const total = 20
	for i := 0; i < total; i++ {
		if _, err := l.Append([]byte(fmt.Sprintf("record-%02d", i))); err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
		// Read back every record written so far, after every single append.
		for j := 0; j <= i; j++ {
			got, err := l.Read(uint64(j))
			if err != nil {
				t.Fatalf("Read(%d) after append %d: %v", j, i, err)
			}
			want := fmt.Sprintf("record-%02d", j)
			if string(got) != want {
				t.Fatalf("Read(%d) after append %d: got %q, want %q", j, i, got, want)
			}
		}
	}
	l.Close()

	l2, err := NewLog(dir, 50, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog reopen failed: %v", err)
	}
	defer l2.Close()

	for i := 0; i < total; i++ {
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

func TestReadEmptyData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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
	l, err := NewLog(dir, 50, SyncConfig{Mode: SyncNone})
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
	l, err := NewLog(dir, 14, SyncConfig{Mode: SyncNone})
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

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

	l2, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	l.Append([]byte("first"))
	l.Append([]byte("second"))
	l.Close()

	l2, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog reopen failed: %v", err)
	}
	l2.Append([]byte("third"))
	l2.Append([]byte("fourth"))
	l2.Close()

	l3, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

	l, err := NewLog(dir, 50, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	for i := 0; i < 10; i++ {
		l.Append([]byte(fmt.Sprintf("record-%02d", i)))
	}
	l.Close()

	l2, err := NewLog(dir, 50, SyncConfig{Mode: SyncNone})
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

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	l.Close()

	l2, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

	l2, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
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
	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	l.Append([]byte("hello world"))
	l.Close()

	// Reopen and check size.
	l2, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog reopen failed: %v", err)
	}
	defer l2.Close()

	expectedSize := int64(RecordSize(len("hello world")))
	if l2.Size() != expectedSize {
		t.Fatalf("size after reopen: got %d, want %d", l2.Size(), expectedSize)
	}
}

func TestSyncPerWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	cfg := SyncConfig{Mode: SyncPerWrite}
	l, err := NewLog(dir, 0, cfg)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	// Write records. Each one should be synced to disk immediately.
	for i := 0; i < 5; i++ {
		_, err := l.Append([]byte(fmt.Sprintf("record-%d", i)))
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	// Read them all back.
	for i := 0; i < 5; i++ {
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

func TestSyncBatched(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	cfg := SyncConfig{
		Mode:          SyncBatched,
		BatchRecords:  3,
		BatchInterval: 100 * time.Millisecond,
	}
	l, err := NewLog(dir, 0, cfg)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	// Write 10 records. Syncs should happen every 3 records.
	for i := 0; i < 10; i++ {
		_, err := l.Append([]byte(fmt.Sprintf("record-%d", i)))
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	// All records should be readable.
	for i := 0; i < 10; i++ {
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

func TestSyncBatchedTimeInterval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	cfg := SyncConfig{
		Mode:          SyncBatched,
		BatchRecords:  1000, // high threshold so count doesn't trigger
		BatchInterval: 50 * time.Millisecond,
	}
	l, err := NewLog(dir, 0, cfg)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	// Write a few records (won't hit the 1000 record threshold).
	for i := 0; i < 5; i++ {
		l.Append([]byte(fmt.Sprintf("record-%d", i)))
	}

	// Wait for the time-based sync to fire.
	time.Sleep(100 * time.Millisecond)

	l.Close()

	// Reopen and verify records survived (they were synced by the timer).
	l2, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog reopen failed: %v", err)
	}
	defer l2.Close()

	for i := 0; i < 5; i++ {
		got, err := l2.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d) after reopen: %v", i, err)
		}
		want := fmt.Sprintf("record-%d", i)
		if string(got) != want {
			t.Errorf("Read(%d): got %q, want %q", i, got, want)
		}
	}
}

func TestSyncNone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	cfg := SyncConfig{Mode: SyncNone}
	l, err := NewLog(dir, 0, cfg)
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	for i := 0; i < 5; i++ {
		_, err := l.Append([]byte(fmt.Sprintf("record-%d", i)))
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	for i := 0; i < 5; i++ {
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

func TestCrashRecoveryTruncatesIncompleteRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	l.Append([]byte("record one"))
	l.Append([]byte("record two"))
	l.Close()

	// Simulate a crash mid-write by appending garbage to the segment file.
	segPath := filepath.Join(dir, "segment-000001.log")
	f, err := os.OpenFile(segPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open segment: %v", err)
	}
	// Write a partial header (only 5 bytes of a 12-byte header).
	f.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x05})
	f.Close()

	// Get file size before recovery.
	beforeInfo, _ := os.Stat(segPath)
	beforeSize := beforeInfo.Size()

	// Reopen. buildIndex should truncate the incomplete record.
	l2, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog after crash: %v", err)
	}

	// Get file size after recovery.
	afterInfo, _ := os.Stat(segPath)
	afterSize := afterInfo.Size()

	if afterSize >= beforeSize {
		t.Errorf("expected file to shrink: before=%d, after=%d", beforeSize, afterSize)
	}

	t.Logf("file size: before=%d, after=%d (truncated %d bytes)",
		beforeSize, afterSize, beforeSize-afterSize)

	// Both valid records should be readable.
	got, err := l2.Read(0)
	if err != nil {
		t.Fatalf("Read(0): %v", err)
	}
	if string(got) != "record one" {
		t.Errorf("Read(0): got %q, want %q", got, "record one")
	}

	got, err = l2.Read(1)
	if err != nil {
		t.Fatalf("Read(1): %v", err)
	}
	if string(got) != "record two" {
		t.Errorf("Read(1): got %q, want %q", got, "record two")
	}

	// No third record.
	_, err = l2.Read(2)
	if err == nil {
		t.Fatal("expected error reading offset 2")
	}

	l2.Close()
}

func TestCrashRecoveryTruncatesCorruptedRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	l.Append([]byte("good record"))
	l.Append([]byte("will be corrupted"))
	l.Close()

	// Corrupt the second record's data.
	segPath := filepath.Join(dir, "segment-000001.log")

	// Record 0: 12 + 11 = 23 bytes
	// Record 1 starts at byte 23, corrupt byte 23 + 12 + 2 = byte 37
	raw, err := os.OpenFile(segPath, os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open raw file: %v", err)
	}
	corruptPos := int64(23 + headerSize + 2)
	oneByte := make([]byte, 1)
	raw.ReadAt(oneByte, corruptPos)
	oneByte[0] ^= 0xFF
	raw.WriteAt(oneByte, corruptPos)
	raw.Close()

	beforeInfo, _ := os.Stat(segPath)
	beforeSize := beforeInfo.Size()

	// Reopen. Should truncate the corrupted record.
	l2, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog after corruption: %v", err)
	}

	afterInfo, _ := os.Stat(segPath)
	afterSize := afterInfo.Size()

	t.Logf("file size: before=%d, after=%d (truncated %d bytes)",
		beforeSize, afterSize, beforeSize-afterSize)

	// Only the first record should survive.
	got, err := l2.Read(0)
	if err != nil {
		t.Fatalf("Read(0): %v", err)
	}
	if string(got) != "good record" {
		t.Errorf("Read(0): got %q, want %q", got, "good record")
	}

	_, err = l2.Read(1)
	if err == nil {
		t.Fatal("expected error reading corrupted record 1")
	}

	l2.Close()
}

func TestCrashRecoveryThenAppend(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	l.Append([]byte("record one"))
	l.Append([]byte("record two"))
	l.Close()

	// Append garbage to simulate crash.
	segPath := filepath.Join(dir, "segment-000001.log")
	f, _ := os.OpenFile(segPath, os.O_WRONLY|os.O_APPEND, 0644)
	f.Write([]byte("this is garbage from a partial write"))
	f.Close()

	// Reopen. Truncation happens.
	l2, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog after crash: %v", err)
	}

	// Append new records after recovery.
	l2.Append([]byte("record three"))
	l2.Append([]byte("record four"))
	l2.Close()

	// Reopen again and verify everything.
	l3, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog final reopen: %v", err)
	}
	defer l3.Close()

	expected := []string{"record one", "record two", "record three", "record four"}
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

// corruptByte flips every bit of one byte at pos in the named segment.
func corruptByte(t *testing.T, dir, segName string, pos int64) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, segName), os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open %s: %v", segName, err)
	}
	defer f.Close()

	b := make([]byte, 1)
	if _, err := f.ReadAt(b, pos); err != nil {
		t.Fatalf("read %s at %d: %v", segName, pos, err)
	}
	b[0] ^= 0xFF
	if _, err := f.WriteAt(b, pos); err != nil {
		t.Fatalf("write %s at %d: %v", segName, pos, err)
	}
}

// segmentSizes returns the on-disk size of every segment file, by name.
func segmentSizes(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	sizes := make(map[string]int64)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		sizes[e.Name()] = info.Size()
	}
	return sizes
}

// TestRecoveryRefusesMidLogCorruption covers the case where a segment is
// damaged while later segments still hold valid records.
//
// Truncating the damaged segment would discard the records after the damage
// and shift every record in the later segments down to a lower global offset,
// silently, because offsets are derived by counting rather than stored. The
// log must refuse to open instead.
func TestRecoveryRefusesMidLogCorruption(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	// "record-N" is 20 bytes on disk; 40 byte segments hold two records each.
	l, err := NewLog(dir, 40, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := l.Append([]byte(fmt.Sprintf("record-%d", i))); err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}
	l.Close()

	// Damage global offset 2, the first record of the second segment.
	// Offsets 4 through 7 live in later segments and are untouched.
	corruptByte(t, dir, "segment-000002.log", int64(headerSize+2))

	before := segmentSizes(t, dir)

	l2, err := NewLog(dir, 40, SyncConfig{Mode: SyncNone})
	if err == nil {
		l2.Close()
		t.Fatal("NewLog succeeded on mid-log corruption; expected it to refuse")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("error does not wrap ErrCorrupt: %v", err)
	}
	t.Logf("refused with: %v", err)

	// Refusing must not destroy anything: the operator may still want to
	// inspect these files or copy the good segments off.
	after := segmentSizes(t, dir)
	if len(before) != len(after) {
		t.Fatalf("segment count changed: before %d, after %d", len(before), len(after))
	}
	for name, want := range before {
		if got := after[name]; got != want {
			t.Errorf("%s was modified: %d bytes before, %d after", name, want, got)
		}
	}
}

// TestRecoveryTruncatesWhenNothingFollows checks the other half of the rule.
// The same damage is safe to truncate when no valid records exist after it,
// even though the damaged segment is not the last file in the directory.
func TestRecoveryTruncatesWhenNothingFollows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 40, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	for i := 0; i < 4; i++ {
		if _, err := l.Append([]byte(fmt.Sprintf("record-%d", i))); err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}
	l.Close()

	// An empty third segment, as a crash between rotation and the first write
	// of the new segment would leave behind. It holds no records, so nothing
	// follows the damage below.
	empty, err := os.Create(filepath.Join(dir, "segment-000003.log"))
	if err != nil {
		t.Fatalf("create empty segment: %v", err)
	}
	empty.Close()

	// Damage the second record of segment 2, i.e. the last record in the log.
	corruptByte(t, dir, "segment-000002.log", int64(RecordSize(len("record-3"))+headerSize+2))

	l2, err := NewLog(dir, 40, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog should have recovered, got: %v", err)
	}
	defer l2.Close()

	for i := 0; i < 3; i++ {
		got, err := l2.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d): %v", i, err)
		}
		want := fmt.Sprintf("record-%d", i)
		if string(got) != want {
			t.Errorf("Read(%d): got %q, want %q", i, got, want)
		}
	}
	if _, err := l2.Read(3); err == nil {
		t.Error("expected offset 3 to be gone after truncation")
	}
}

// TestRecoveryPreservesOffsetsAfterTailTruncation is the property that the
// mid-log refusal exists to protect: a record's offset must name the same
// record before and after a crash.
func TestRecoveryPreservesOffsetsAfterTailTruncation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	l, err := NewLog(dir, 40, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := l.Append([]byte(fmt.Sprintf("record-%d", i))); err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}
	l.Close()

	// A partial write at the very end, the ordinary crash case.
	f, err := os.OpenFile(filepath.Join(dir, "segment-000004.log"), os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open last segment: %v", err)
	}
	f.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x05})
	f.Close()

	l2, err := NewLog(dir, 40, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog after crash: %v", err)
	}
	defer l2.Close()

	// Every surviving offset must still name the record it named before.
	for i := 0; i < 8; i++ {
		got, err := l2.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d): %v", i, err)
		}
		want := fmt.Sprintf("record-%d", i)
		if string(got) != want {
			t.Errorf("offset %d changed meaning: got %q, want %q", i, got, want)
		}
	}
}

func TestCrashRecoveryMultipleSegments(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	// Small segments to force rotation.
	l, err := NewLog(dir, 50, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog failed: %v", err)
	}

	for i := 0; i < 6; i++ {
		l.Append([]byte(fmt.Sprintf("record-%d", i)))
	}
	l.Close()

	// Find the last segment file and corrupt it.
	entries, _ := os.ReadDir(dir)
	var lastSeg string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log") {
			lastSeg = e.Name()
		}
	}

	segPath := filepath.Join(dir, lastSeg)
	f, _ := os.OpenFile(segPath, os.O_WRONLY|os.O_APPEND, 0644)
	f.Write([]byte("garbage after crash"))
	f.Close()

	// Reopen. Should truncate garbage from last segment only.
	l2, err := NewLog(dir, 50, SyncConfig{Mode: SyncNone})
	if err != nil {
		t.Fatalf("NewLog after crash: %v", err)
	}
	defer l2.Close()

	// All 6 original records should be intact.
	for i := 0; i < 6; i++ {
		got, err := l2.Read(uint64(i))
		if err != nil {
			t.Fatalf("Read(%d): %v", i, err)
		}
		want := fmt.Sprintf("record-%d", i)
		if string(got) != want {
			t.Errorf("Read(%d): got %q, want %q", i, got, want)
		}
	}
}
