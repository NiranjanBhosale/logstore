package log

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// defaultMaxSegSize is the default maximum segment file size (10 MB).
	defaultMaxSegSize = 10 * 1024 * 1024
)

// Segment holds metadata about one segment file.
type Segment struct {
	name  string  // filename
	index []int64 // byte position of each record within this segment
	size  int64   // total bytes written
}

// Log is an append-only log stored across multiple segment files.
// Records are identified by a global offset (a monotonically increasing uint64)
// assigned at append time. The log manages segment rotation, configurable
// durability via fsync modes, and crash recovery on startup.
//
// Log is safe for concurrent use by multiple goroutines.
type Log struct {
	mu sync.Mutex

	dir        string
	segments   []*os.File
	segInfo    []Segment
	current    *os.File
	writer     *bufio.Writer
	maxSegSize int64
	numRecords uint64

	syncCfg       SyncConfig
	unflushedRecs int           // records written since last sync
	stopChan      chan struct{} // signals the batch sync goroutine to stop
	doneChan      chan struct{} // signals the batch sync goroutine has stopped
}

// NewLog opens or creates a log in the given directory.
//
// If the directory already contains segment files, NewLog scans them to rebuild
// the in-memory index and truncates any corrupted or incomplete records at the
// tail of each segment (crash recovery).
//
// maxSegSize controls when a new segment file is created. Use 0 for the default
// of 10 MB. syncCfg controls the fsync durability mode.
func NewLog(dir string, maxSegSize int64, syncCfg SyncConfig) (*Log, error) {
	if maxSegSize <= 0 {
		maxSegSize = defaultMaxSegSize
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log directory %q: %w", dir, err)
	}

	l := &Log{
		dir:        dir,
		segments:   make([]*os.File, 0),
		segInfo:    make([]Segment, 0),
		maxSegSize: maxSegSize,
		syncCfg:    syncCfg,
	}

	if err := l.discoverSegments(); err != nil {
		return nil, fmt.Errorf("discover segments: %w", err)
	}

	if err := l.buildIndex(); err != nil {
		l.closeAll()
		return nil, fmt.Errorf("build index: %w", err)
	}

	// If no segments exist, create the first one.
	if len(l.segments) == 0 {
		if err := l.newSegment(); err != nil {
			return nil, fmt.Errorf("create first segment: %w", err)
		}
	} else {
		// Point writer at the last segment, seeked to the end.
		lastIdx := len(l.segments) - 1
		l.current = l.segments[lastIdx]
		lastSize := l.segInfo[lastIdx].size
		if _, err := l.current.Seek(lastSize, io.SeekStart); err != nil {
			l.closeAll()
			return nil, fmt.Errorf("seek to end of last segment: %w", err)
		}
		l.writer = bufio.NewWriter(l.current)
	}

	// Start the batch sync goroutine if needed.
	if l.syncCfg.Mode == SyncBatched {
		l.stopChan = make(chan struct{})
		l.doneChan = make(chan struct{})
		go l.batchSyncLoop()
	}

	return l, nil
}

// segmentName returns the filename for a segment number (1-based).
func (l *Log) segmentName(segNum int) string {
	return filepath.Join(l.dir, fmt.Sprintf("segment-%06d.log", segNum))
}

// discoverSegments finds and opens all existing segment files in sorted order.
func (l *Log) discoverSegments() error {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}

	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "segment-") && strings.HasSuffix(e.Name(), ".log") {
			names = append(names, e.Name())
		}
	}

	sort.Strings(names)

	for _, name := range names {
		numStr := strings.TrimSuffix(strings.TrimPrefix(name, "segment-"), ".log")
		if _, err := strconv.Atoi(numStr); err != nil {
			return fmt.Errorf("parse segment number from %q: %w", name, err)
		}

		path := filepath.Join(l.dir, name)
		seg, err := os.OpenFile(path, os.O_RDWR, 0644)
		if err != nil {
			return fmt.Errorf("open segment %q: %w", name, err)
		}

		l.segments = append(l.segments, seg)
		l.segInfo = append(l.segInfo, Segment{
			name:  name,
			index: make([]int64, 0),
		})
	}

	return nil
}

// newSegment creates a new segment file and makes it the current one.
func (l *Log) newSegment() error {
	segNum := len(l.segments) + 1
	name := l.segmentName(segNum)

	seg, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("create segment %d: %w", segNum, err)
	}

	l.segments = append(l.segments, seg)
	l.segInfo = append(l.segInfo, Segment{
		name:  filepath.Base(name),
		index: make([]int64, 0),
	})
	l.current = seg
	l.writer = bufio.NewWriter(seg)

	return nil
}

// closeAll closes all open segment files. Used during error cleanup.
func (l *Log) closeAll() {
	for _, seg := range l.segments {
		if seg != nil {
			seg.Close()
		}
	}
}

// buildIndex scans all segment files, validates each record's checksum,
// and populates the per-segment in-memory index. Corrupted or incomplete
// records at the tail of any segment are truncated.
func (l *Log) buildIndex() error {
	l.numRecords = 0

	for segIdx, seg := range l.segments {
		if _, err := seg.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seek segment %d: %w", segIdx, err)
		}

		// Get file size so we can validate dataLen values.
		fileInfo, err := seg.Stat()
		if err != nil {
			return fmt.Errorf("stat segment %d: %w", segIdx, err)
		}
		fileSize := fileInfo.Size()

		l.segInfo[segIdx].index = make([]int64, 0)
		var segSize int64

		for {
			bytePos := segSize

			header := make([]byte, headerSize)
			_, err := io.ReadFull(seg, header)

			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			if err != nil {
				return fmt.Errorf("read header in segment %d at byte %d: %w",
					segIdx, bytePos, err)
			}

			dataLen := binary.BigEndian.Uint64(header[0:lenSize])

			// Sanity check: dataLen cannot exceed remaining bytes in the file.
			remaining := fileSize - bytePos - int64(headerSize)

			if dataLen > math.MaxInt32 || int64(dataLen) > remaining || int64(dataLen) < 0 {
				break
			}

			data := make([]byte, int(dataLen))
			if dataLen > 0 {
				_, err = io.ReadFull(seg, data)
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					break
				}
				if err != nil {
					return fmt.Errorf("read data in segment %d at byte %d: %w",
						segIdx, bytePos, err)
				}
			}

			storedChecksum := binary.BigEndian.Uint32(header[lenSize:headerSize])
			if storedChecksum != crc32.ChecksumIEEE(data) {
				break
			}

			l.segInfo[segIdx].index = append(l.segInfo[segIdx].index, bytePos)
			segSize += int64(headerSize) + int64(dataLen)
		}

		if fileInfo.Size() > segSize {
			if err := seg.Truncate(segSize); err != nil {
				return fmt.Errorf("truncate segment %d: %w", segIdx, err)
			}
		}

		l.segInfo[segIdx].size = segSize
		l.numRecords += uint64(len(l.segInfo[segIdx].index))
	}

	return nil
}

// Append writes data to the log and returns the global offset assigned to the record.
// The offset can later be passed to Read to retrieve the data.
//
// If the current segment exceeds maxSegSize, a new segment is created automatically.
// Depending on the configured SyncMode, Append may call file.Sync() before returning.
func (l *Log) Append(data []byte) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Check if current segment needs rotation.
	lastIdx := len(l.segments) - 1
	if l.segInfo[lastIdx].size >= l.maxSegSize {
		// Flush and optionally sync the old segment before rotating.
		if l.syncCfg.Mode == SyncBatched {
			if err := l.sync(); err != nil {
				return 0, fmt.Errorf("sync before rotation: %w", err)
			}
		} else {
			if err := l.writer.Flush(); err != nil {
				return 0, fmt.Errorf("flush before rotation: %w", err)
			}
		}
		if err := l.newSegment(); err != nil {
			return 0, fmt.Errorf("rotate segment: %w", err)
		}
		lastIdx = len(l.segments) - 1
	}

	record := Encode(data)
	bytePos := l.segInfo[lastIdx].size

	n, err := l.writer.Write(record)
	if err != nil {
		return 0, fmt.Errorf("append record: %w", err)
	}

	l.segInfo[lastIdx].index = append(l.segInfo[lastIdx].index, bytePos)
	l.segInfo[lastIdx].size += int64(n)

	offset := l.numRecords
	l.numRecords++
	l.unflushedRecs++

	// Sync based on mode.
	switch l.syncCfg.Mode {
	case SyncPerWrite:
		if err := l.sync(); err != nil {
			return 0, fmt.Errorf("per-write sync: %w", err)
		}
	case SyncBatched:
		if l.unflushedRecs >= l.syncCfg.BatchRecords {
			if err := l.sync(); err != nil {
				return 0, fmt.Errorf("batch sync: %w", err)
			}
		}
		// Time-based sync is handled by batchSyncLoop.
	case SyncNone:
		// No sync. Data sits in bufio.Writer and OS page cache.
	}

	return offset, nil
}

// Read retrieves the data for the record at the given global offset.
// It validates the CRC32 checksum and returns an error if the data is corrupted.
func (l *Log) Read(offset uint64) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if offset >= l.numRecords {
		return nil, fmt.Errorf("offset %d out of range: log has %d records",
			offset, l.numRecords)
	}

	if err := l.writer.Flush(); err != nil {
		return nil, fmt.Errorf("flush before read: %w", err)
	}

	// Find which segment this offset belongs to.
	segIdx, localOffset := l.locateRecord(offset)

	seg := l.segments[segIdx]
	info := l.segInfo[segIdx]
	bytePos := info.index[localOffset]

	// Figure out record size.
	var recordSize int64
	if int(localOffset+1) < len(info.index) {
		// Not the last record in this segment.
		recordSize = info.index[localOffset+1] - bytePos
	} else {
		// Last record in this segment.
		recordSize = info.size - bytePos
	}

	// Seek and read.
	if _, err := seg.Seek(bytePos, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek to offset %d: %w", offset, err)
	}

	record := make([]byte, recordSize)
	if _, err := io.ReadFull(seg, record); err != nil {
		return nil, fmt.Errorf("read record at offset %d: %w", offset, err)
	}

	return Decode(record)
}

// locateRecord maps a global offset to a segment index and local offset within that segment.
func (l *Log) locateRecord(offset uint64) (segIdx int, localOffset uint64) {
	remaining := offset
	for i, info := range l.segInfo {
		count := uint64(len(info.index))
		if remaining < count {
			return i, remaining
		}
		remaining -= count
	}
	// Should never reach here if offset was validated.
	return 0, 0
}

// Size returns the total number of bytes of valid data across all segments.
func (l *Log) Size() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var total int64
	for _, info := range l.segInfo {
		total += info.size
	}
	return total
}

// Close stops any background sync goroutine, flushes buffered data,
// and closes all segment files.
func (l *Log) Close() error {
	// Stop the batch sync goroutine if it's running.
	if l.syncCfg.Mode == SyncBatched && l.stopChan != nil {
		close(l.stopChan) // signal the goroutine to stop
		<-l.doneChan      // wait for it to actually stop
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Final sync to push everything to disk.
	if l.writer != nil {
		if err := l.writer.Flush(); err != nil {
			return fmt.Errorf("flush writer: %w", err)
		}
	}

	var firstErr error
	for _, seg := range l.segments {
		if seg != nil {
			if err := seg.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

// sync flushes the bufio.Writer and then calls file.Sync() on the current segment.
// Must be called with l.mu held.
func (l *Log) sync() error {
	if l.writer == nil || l.current == nil {
		return nil
	}

	if err := l.writer.Flush(); err != nil {
		return fmt.Errorf("flush writer: %w", err)
	}

	if err := l.current.Sync(); err != nil {
		return fmt.Errorf("sync file: %w", err)
	}

	l.unflushedRecs = 0
	return nil
}

// batchSyncLoop runs in a background goroutine and periodically syncs
// based on the configured interval.
func (l *Log) batchSyncLoop() {
	ticker := time.NewTicker(l.syncCfg.BatchInterval)
	defer ticker.Stop()
	defer close(l.doneChan)

	for {
		select {
		case <-ticker.C:
			l.mu.Lock()
			if l.unflushedRecs > 0 {
				l.sync()
			}
			l.mu.Unlock()
		case <-l.stopChan:
			return
		}
	}
}
