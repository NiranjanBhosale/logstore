# logstore

An append-only log storage engine in Go, with configurable durability and crash recovery, plus a primary/backup replication layer built on top of it.

For the reasoning behind the design — the invariants it holds, why recovery works the way it does, and an honest account of what it gets wrong — see **[DESIGN.md](DESIGN.md)**.

## Motivation

Append-only logs are the storage primitive underneath Kafka, etcd's write-ahead log, and most backup chain systems. This is a single-node implementation built to explore the central tradeoff in storage engineering: **durability against write throughput**. Every `fsync` guarantees survival of a crash and costs milliseconds. How that tradeoff is managed decides whether a system handles a hundred writes per second or a hundred thousand.

The replication layer on top is deliberately naive — one configured primary, no elections — built to make the failures that consensus protocols solve happen in code I wrote, before reading the protocols.

## Architecture

```
log directory
├── segment-000001.log   (max 10 MB)
├── segment-000002.log   (max 10 MB)
└── segment-000003.log   (current, accepting writes)
```

### Record format

A fixed 12-byte header followed by the payload:

```
┌───────────────────────┬──────────────┬────────────────────┐
│  data length (8 B)    │  CRC32 (4 B) │  payload (N bytes) │
│  big-endian uint64    │  IEEE poly   │  raw bytes         │
└───────────────────────┴──────────────┴────────────────────┘
```

Nothing on disk records where records begin. The length field is the only way to find the next record, which makes a segment a linked list and drives the recovery rules below.

### Addressing

`Append` returns a global `uint64` offset. `Read` resolves it through an in-memory index of byte positions, one slice per segment:

```
global offset → locateRecord() → (segment index, local offset)
              → segInfo[segment].index[local] = byte position
              → ReadAt(position) → Decode
```

Offsets are **derived, not stored** — a record's offset is its position in the sequence, counted at startup. That has consequences for recovery; see [DESIGN.md §5](DESIGN.md#5-crash-recovery).

## Usage

```go
package main

import (
    "fmt"
    "log"
    "time"

    logstore "github.com/NiranjanBhosale/logstore/internal/log"
)

func main() {
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

    offset, err := l.Append([]byte("user:1001 action:login"))
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("wrote record at offset %d\n", offset)

    data, err := l.Read(offset)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("read: %s\n", data)
}
```

### Running the replicated pair

```bash
go run ./cmd/follower -addr :50051 -dir ./follower-data
```

```bash
go run ./cmd/primary -peer localhost:50051 -dir ./primary-data -count 20
```

Both logs should then be byte-for-byte identical:

```bash
cmp primary-data/segment-000001.log follower-data/segment-000001.log && echo identical
```

## Design decisions

**Why append-only.** No in-place updates means no fragmentation, no free-space manager, sequential I/O throughout, and recovery that only has to scan forward.

**Why CRC32-IEEE.** The threat is hardware — bit flips, torn writes, interrupted writes — not adversaries. CRC32 detects those reliably at roughly a gigabyte per second. A cryptographic hash costs more and defends against an attacker who has already won by the time they can rewrite segment files.

**Why segment rotation.** A single growing file makes startup scan time unbounded, makes deleting old data require rewriting everything, and degrades on most filesystems at large sizes. Segments cap each file at a configurable size and make future compaction possible.

**Why offsets must stay stable.** Offset *N* has to name the same record forever — that is what makes an offset a durable name you can hand to another process, or another machine. Recovery is constrained by this; it will refuse to open a log rather than quietly renumber records.

## Crash recovery

On open, every segment is scanned and every checksum verified. A scan stops at the first record that is incomplete or fails its checksum — it must, because a corrupt record's length field cannot be trusted, and that field is the only route to the records after it.

What happens next depends on where the damage is:

| Damage location | Behaviour |
|---|---|
| Tail of the log, nothing valid after it | Truncate to the last valid record and open normally |
| Anywhere with valid records after it | Return `ErrCorrupt`, leave every file untouched |

The rule is that **truncation is safe only when no valid records exist after the truncation point.** Because offsets are counted rather than stored, discarding records from the middle would not leave a gap — it would silently shift every later record to a lower offset, so code that saved "offset 7" would afterwards read a different record under that name.

An interrupted write satisfies the rule trivially, which is why ordinary crash recovery works. Anything else does not, so the log refuses to open and preserves the files for inspection or restoration from a replica. `ErrCorrupt` is a sentinel, so callers can distinguish it with `errors.Is`.

## Performance

Apple M4 Pro, macOS 26.6 (APFS), Go 1.26.3, `-benchtime=2s`. Reproduce with:

```bash
go test ./internal/log/ -bench=. -benchmem -run=XXX -benchtime=2s
```

### Append throughput

| Mode | 64 B | 1 KB | 64 KB | 1 MB |
|---|---|---|---|---|
| `SyncNone` | 800 MB/s | 1,196 MB/s | 3,113 MB/s | 3,143 MB/s |
| `SyncBatched` | 1.25 MB/s | 13.9 MB/s | 285 MB/s | 372 MB/s |
| `SyncPerWrite` | 0.02 MB/s | 0.25 MB/s | 15.7 MB/s | 186 MB/s |

### Append latency, 1 KB records

| Mode | p50 | p95 | p99 |
|---|---|---|---|
| `SyncNone` | 334 ns | 7.42 µs | 9.96 µs |
| `SyncBatched` | 500 ns | 25.5 µs | 3.49 ms |
| `SyncPerWrite` | 3.99 ms | 4.13 ms | 5.02 ms |

### Read throughput

| Access pattern | 64 B | 1 KB | 64 KB |
|---|---|---|---|
| Sequential | 218 MB/s | 2,144 MB/s | 6,100 MB/s |
| Random | — | 1,850 MB/s | — |

Random and sequential reads are close because the working set fits in the page cache; this is not a measurement of random-access performance against cold storage.

### Reading the numbers

**The bottleneck is one syscall.** Four orders of magnitude separate `SyncNone` from `SyncPerWrite` at 64 B, and the difference is entirely `fsync` — not data transfer. Batching buys back roughly **79×** over per-write sync for small records (4.02 ms/op against 51.1 µs/op at 64 B).

**`SyncBatched`'s p99 is about 7,000× its p50.** Most appends land in a buffer and return in half a microsecond; one append per batch pays the entire `fsync`. Batching concentrates the cost on an unlucky caller rather than removing it.

**Platform matters for the `fsync` figure.** Go's `File.Sync()` on macOS issues `fcntl(F_FULLFSYNC)`, forcing a full drive cache flush to stable media. Linux's `fdatasync` typically returns in 50–500 µs on NVMe because it may leave data in the drive's volatile write cache. Same Go source, an order of magnitude apart, weaker guarantee. The ~4 ms here is the strong one.

### Replication

One record replicated end-to-end costs **6.5–8.6 ms** — the primary's `fsync`, the follower's `fsync`, and a round trip. Against a 3.99 ms `fsync`, the two syncs account for essentially all of it; the localhost round trip does not appear in the measurement. That is why records are currently sent one per RPC despite the protocol supporting batches.

## What this does not do

Current limitations, all deliberate at this stage and detailed in [DESIGN.md §8](DESIGN.md#8-what-fails):

- **Replicated logs can diverge silently.** A failed send leaves the record on the primary alone, and nothing detects or reconciles it. A restarted follower never catches up.
- **A batch can apply partially**, and the response has no way to say so.
- **Duplicates are stored.** Nothing identifies a request, so a retry is stored twice.
- **No quorum, elections, or failure detection.** One follower; two nodes are currently less available than one.
- **Startup is O(total bytes)** — the index is rebuilt by full replay. Hint files are the known fix.

## Project structure

```
logstore/
├── cmd/
│   ├── logstore/main.go              # single-node demo
│   ├── follower/main.go              # replication follower
│   └── primary/main.go               # replication primary
├── internal/
│   ├── log/
│   │   ├── record.go                 # record encoding and decoding
│   │   ├── log.go                    # Log, Append, Read, crash recovery
│   │   ├── sync.go                   # SyncMode and SyncConfig
│   │   └── *_test.go                 # format, log, recovery, benchmarks
│   └── replication/
│       ├── follower.go               # gRPC server, appends to its own log
│       ├── primary.go                # gRPC client, appends then replicates
│       ├── replication_test.go       # in-process two-node tests
│       └── replicationpb/            # generated, do not edit
├── proto/replication/v1/replication.proto
├── Makefile                          # make proto
├── DESIGN.md
└── README.md
```

## Development

```bash
go test ./... -race
```

```bash
make proto
```

`make proto` regenerates the gRPC stubs. It needs `protoc` plus `protoc-gen-go` and `protoc-gen-go-grpc` on `PATH`.
