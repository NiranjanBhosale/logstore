# LogStore

A single-node append-only log store in Go with configurable durability and crash recovery.

## Motivation

Append-only logs are the foundational storage primitive behind systems like Apache Kafka, etcd's write-ahead log, and backup chain systems. This project implements a single-node version to explore the core tradeoff in storage systems: **durability vs. write throughput**. Every `fsync` call guarantees data survives a crash but adds milliseconds of latency. How you manage that tradeoff determines whether your system handles 100 or 100,000 writes per second.

## Architecture

```
Log Directory
├── segment-000001.log (max 10MB)
├── segment-000002.log (max 10MB)
└── segment-000003.log (current, accepting writes)
```

### Record Format

Each record on disk has a fixed 12-byte header followed by variable-length data:

```
┌──────────────────────┬──────────────┬─────────────────────┐
│ Data Length (8B) │ CRC32 (4B) │ Data (N bytes) │
│ big-endian uint64 │ IEEE poly │ raw bytes │
└──────────────────────┴──────────────┴─────────────────────┘
12 bytes header variable
```

### In-Memory Index

Each segment maintains its own index mapping local record numbers to byte positions:

Global Offset → locateRecord() → (SegmentIndex, LocalOffset)
↓
segInfo[SegmentIndex].index[LocalOffset] = BytePos
↓
Seek to BytePos in segment file → Read → Decode


## Usage

```go
package main

import (
    "fmt"
    "log"

    logstore "github.com/NiranjanBhosale/logstore/internal/log"
)

func main() {
    // Create a log with 10MB segments and batched fsync
    cfg := logstore.SyncConfig{
        Mode:          logstore.SyncBatched,
        BatchRecords:  100,
        BatchInterval: 10 * time.Millisecond,
    }
    l, err := logstore.NewLog("./data", 10*1024*1024, cfg)
    if err != nil {
        log.Fatal(err)
    }
    defer l.Close()

    // Append records
    offset, _ := l.Append([]byte("user:1001 action:login"))
    fmt.Printf("wrote record at offset %d\n", offset)

    // Read records
    data, _ := l.Read(offset)
    fmt.Printf("read: %s\n", data)
}
```

## Design Decisions

### Why append-only?

Append-only logs avoid the complexity of in-place updates. Every write goes to the end of the current segment, which means:

- No fragmentation
- No need for a free-space manager
- Sequential I/O (the fastest kind on both HDDs and SSDs)
- Simple crash recovery (scan forward, stop at first corruption)

### Why CRC32 checksums?

CRC32-IEEE detects accidental corruption (bit flips, truncated writes, disk errors) with a 1-in-4-billion false positive rate. It computes at ~1 GB/s on modern CPUs with hardware support. We don't need cryptographic guarantees (SHA-256) because we're protecting against hardware faults, not adversaries.

### Why segment rotation?

A single growing file causes problems at scale:

- Startup scan time grows linearly with file size
- Old data can't be deleted without rewriting the entire file
- Filesystem performance degrades for very large files

Segments cap each file at a configurable size (default 10MB), keeping startup fast and enabling future log compaction.

The fsync tradeoff

| Mode | Throughput (64B) | Durability | Latency (p50) |
| :--- | :--- | :--- | :--- |
| SyncNone | ~724 MB/s | Lowest — OS decides when to flush | ~1 µs |
| SyncBatched | ~1.2 MB/s | Medium — sync every N records or T ms | ~50 µs |
| SyncPerWrite | ~0.02 MB/s | Highest — every write on disk | ~4 ms |

The bottleneck is the fsync syscall (~4ms on SSD), not data transfer. Batching amortizes this cost across multiple records, achieving 78x higher throughput than per-write sync for small records.

How crash recovery works
On startup, NewLog scans each segment file sequentially:

1. Read the 12-byte header
2. Validate the claimed data length against remaining file bytes
3. Read the data and verify the CRC32 checksum
4. If any check fails, stop scanning and truncate the file at the last valid record
5. Rebuild the in-memory index from valid records only

This ensures that after a crash, the log contains only complete, uncorrupted records.

## Performance Results

Benchmarks run on Apple M-series.

### Write Throughput

| Mode | 64B | 1KB | 64KB | 1MB |
| :--- | :--- | :--- | :--- | :--- |
| SyncNone | 724 MB/s | 914 MB/s | 1,706 MB/s | 1,497 MB/s |
| SyncBatched | 1.2 MB/s | 14 MB/s | 329 MB/s | 464 MB/s |
| SyncPerWrite | 0.02 MB/s | 0.25 MB/s | 16 MB/s | 193 MB/s |

### Read Throughput

| Access Pattern | 64B | 1KB | 64KB |
| :--- | :--- | :--- | :--- |
| Sequential | 124 MB/s | 1,517 MB/s | 6,280 MB/s |
| Random (1KB) | — | 1,513 MB/s | — |

### Write Latency (1KB records)

| Mode | p50 | p95 | p99 |
| :--- | :--- | :--- | :--- |
| SyncNone | ~1 µs | ~4 µs | ~12 µs |
| SyncBatched | ~2 µs | ~290 µs | ~310 µs |
| SyncPerWrite | ~280 µs | ~450 µs | ~1.2 ms |

## Project Structure

```
logstore/
├── cmd/logstore/main.go          # Demo application
├── internal/log/
│   ├── record.go                 # Binary record encoding/decoding
│   ├── log.go                    # Log struct, Append, Read, crash recovery
│   ├── sync.go                   # SyncMode and SyncConfig types
│   ├── record_test.go            # Record format tests
│   ├── log_test.go               # Log operation and crash recovery tests
│   └── bench_test.go             # Throughput and latency benchmarks
├── go.mod
├── README.md
└── DESIGN.md
```
