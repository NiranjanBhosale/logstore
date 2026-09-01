package log

import (
	"sort"
	"testing"
	"time"
)

func BenchmarkAppendSyncNone(b *testing.B) {
	benchmarkAppend(b, SyncConfig{Mode: SyncNone})
}

func BenchmarkAppendSyncPerWrite(b *testing.B) {
	benchmarkAppend(b, SyncConfig{Mode: SyncPerWrite})
}

func BenchmarkAppendSyncBatched(b *testing.B) {
	benchmarkAppend(b, SyncConfig{
		Mode:          SyncBatched,
		BatchRecords:  100,
		BatchInterval: 10 * time.Millisecond,
	})
}

func benchmarkAppend(b *testing.B, cfg SyncConfig) {
	sizes := []struct {
		name string
		size int
	}{
		{"64B", 64},
		{"1KB", 1024},
		{"64KB", 64 * 1024},
		{"1MB", 1024 * 1024},
	}

	for _, s := range sizes {
		b.Run(s.name, func(b *testing.B) {
			dir := b.TempDir()
			l, err := NewLog(dir, 0, cfg)
			if err != nil {
				b.Fatalf("NewLog failed: %v", err)
			}
			defer l.Close()

			data := make([]byte, s.size)
			// Fill with non-zero data so CRC32 is realistic.
			for i := range data {
				data[i] = byte(i % 251)
			}

			b.ResetTimer()
			b.SetBytes(int64(s.size))

			for i := 0; i < b.N; i++ {
				if _, err := l.Append(data); err != nil {
					b.Fatalf("Append failed: %v", err)
				}
			}
		})
	}
}

func BenchmarkReadSequential(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"64B", 64},
		{"1KB", 1024},
		{"64KB", 64 * 1024},
	}

	for _, s := range sizes {
		b.Run(s.name, func(b *testing.B) {
			dir := b.TempDir()
			l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
			if err != nil {
				b.Fatalf("NewLog failed: %v", err)
			}
			defer l.Close()

			data := make([]byte, s.size)
			for i := range data {
				data[i] = byte(i % 251)
			}

			// Pre-populate the log with enough records.
			numRecords := 10000
			if s.size >= 64*1024 {
				numRecords = 100 // fewer large records
			}
			for i := 0; i < numRecords; i++ {
				l.Append(data)
			}

			b.ResetTimer()
			b.SetBytes(int64(s.size))

			for i := 0; i < b.N; i++ {
				offset := uint64(i % numRecords)
				if _, err := l.Read(offset); err != nil {
					b.Fatalf("Read failed: %v", err)
				}
			}
		})
	}
}

func BenchmarkReadRandom(b *testing.B) {
	dir := b.TempDir()
	l, err := NewLog(dir, 0, SyncConfig{Mode: SyncNone})
	if err != nil {
		b.Fatalf("NewLog failed: %v", err)
	}
	defer l.Close()

	data := make([]byte, 1024)
	for i := range data {
		data[i] = byte(i % 251)
	}

	numRecords := 10000
	for i := 0; i < numRecords; i++ {
		l.Append(data)
	}

	// Generate a pseudo-random access pattern.
	// Not using math/rand to avoid import — simple hash works fine.
	offsets := make([]uint64, b.N)
	for i := range offsets {
		offsets[i] = uint64((i * 7919) % numRecords)
	}

	b.ResetTimer()
	b.SetBytes(1024)

	for i := 0; i < b.N; i++ {
		if _, err := l.Read(offsets[i]); err != nil {
			b.Fatalf("Read failed: %v", err)
		}
	}
}

func TestAppendLatencyDistribution(t *testing.T) {
	modes := []struct {
		name string
		cfg  SyncConfig
	}{
		{"SyncNone", SyncConfig{Mode: SyncNone}},
		{"SyncPerWrite", SyncConfig{Mode: SyncPerWrite}},
		{"SyncBatched", SyncConfig{
			Mode:          SyncBatched,
			BatchRecords:  100,
			BatchInterval: 10 * time.Millisecond,
		}},
	}

	numOps := 1000
	data := make([]byte, 1024) // 1KB records
	for i := range data {
		data[i] = byte(i % 251)
	}

	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			dir := t.TempDir()
			l, err := NewLog(dir, 0, m.cfg)
			if err != nil {
				t.Fatalf("NewLog failed: %v", err)
			}
			defer l.Close()

			latencies := make([]time.Duration, numOps)

			for i := 0; i < numOps; i++ {
				start := time.Now()
				_, err := l.Append(data)
				latencies[i] = time.Since(start)
				if err != nil {
					t.Fatalf("Append failed: %v", err)
				}
			}

			sortDurations(latencies)

			p50 := latencies[numOps*50/100]
			p95 := latencies[numOps*95/100]
			p99 := latencies[numOps*99/100]

			t.Logf("%-14s  p50=%-12s  p95=%-12s  p99=%-12s",
				m.name, p50, p95, p99)
		})
	}
}

// sortDurations sorts a slice of time.Duration in ascending order.
func sortDurations(d []time.Duration) {
	sort.Slice(d, func(i, j int) bool {
		return d[i] < d[j]
	})
}
