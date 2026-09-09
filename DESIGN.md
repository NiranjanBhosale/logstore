# Design

How logstore works, which properties it guarantees, and where it breaks.

This document is written to be read after the [README](README.md). The README says what the project is; this one argues why it is built the way it is, and is honest about what it does not do.

---

## 1. Scope

Two layers, built in that order:

**A single-node append-only log.** Records go on the end of a file, are never modified, and are addressed by a monotonically increasing offset. Segment rotation, an in-memory index, configurable `fsync` policy, and crash recovery.

**Primary/backup replication on top of it.** One configured primary, one follower, gRPC between them. No elections, no consensus.

The replication layer is deliberately naive. It exists to make the failures that consensus protocols solve happen to a system whose code I have read, before I read the protocols. Section 8 lists what it gets wrong on purpose.

---

## 2. On-disk format

Every record is a fixed 12-byte header followed by its payload:

```
┌───────────────────────┬──────────────┬────────────────────┐
│  data length (8 B)    │  CRC32 (4 B) │  payload (N bytes) │
│  big-endian uint64    │  IEEE poly   │  raw bytes         │
└───────────────────────┴──────────────┴────────────────────┘
```

**Why a length prefix.** Nothing else on disk records where records begin. There is no table of positions and no delimiter. The only way to find record *N+1* is to read record *N*'s header and skip `12 + length` bytes. A segment is effectively a singly linked list whose "next" pointer is the length field, and that fact drives all of Section 5.

**Why CRC32-IEEE.** The threat model is hardware, not adversaries: bit flips, torn writes, a write interrupted by power loss. CRC32 detects those reliably and computes at roughly a gigabyte per second with hardware support. A cryptographic hash would cost more and defend against an attacker who, by the time they can rewrite your segment files, has already won.

**Why the checksum covers only the payload.** The length field is validated structurally instead — against the bytes actually remaining in the file — so a corrupt length is caught before it is ever used to slice.

### Segments

Data is split across `segment-NNNNNN.log` files, rotating when the current one reaches `maxSegSize` (default 10 MB). Rotation is checked before a write, so a segment may overshoot by at most one record: **records are never split across files.**

Segments exist so that startup scan time and eventual compaction are bounded by segment size rather than total log size.

---

## 3. Offsets and the index

`Append` returns a `uint64` offset that is global across segments. `Read(offset)` maps it to a segment and a byte position through an in-memory index — one `[]int64` of byte positions per segment — so lookup is O(1) within a segment and O(number of segments) to find the segment.

The index is rebuilt at startup by replaying every segment and validating every checksum. This is the known cost of the design: startup is O(total bytes). Production systems avoid it with hint files (Bitcask) or a checkpointed index. It is a deliberate next optimisation, not an oversight.

**Offsets are derived, not stored.** A record's offset is its position in the sequence, computed at startup by counting the records before it. Nothing on disk says "I am record 4."

That single fact is responsible for the recovery rule in Section 5, and it is the first thing I would change if the format were versioned.

---

## 4. Invariants

These are the properties the log holds, and the reason each exists. Every one of them was violated by an earlier version of this code; the tests that assert them were each verified to fail against the implementation they replaced.

**Offset stability.** Offset *N* names the same record for the life of the log. Offsets are never reused and never renumbered. This is what makes an offset a durable name that can be handed to another process — and once replication exists, to another machine.

**Density.** Offsets are contiguous. There are no holes. A gapless sequence is what makes "my log matches yours through index *N*" a meaningful statement, which is what any log-matching protocol is built on.

**Whole records only.** A reader never sees a partially written record. Recovery discards any trailing bytes that do not form a complete, checksum-valid record.

**Acknowledged means persisted.** Under `SyncPerWrite` and `SyncBatched`, a returned `Append` means the configured durability has actually been met. If it cannot be met, the log stops accepting writes rather than acknowledging records it cannot stand behind (Section 6).

**Reads do not disturb writes.** `Read` uses `ReadAt`, a positional read that never touches the file offset. An earlier version used `Seek` on the same descriptor the buffered writer wrote through; a read left the cursor mid-file and the next flush overwrote live records. No concurrency was required to trigger it.

---

## 5. Crash recovery

On open, each segment is scanned record by record and every checksum verified. A scan **stops at the first record that is incomplete or fails its checksum.**

It has to stop, and the reason is Section 2. When a record's CRC fails, some of its bytes are wrong but there is no way to know *which*. If the damage landed in the length field, that field is now an arbitrary number — and it is the only route to every record after it in that segment. The scanner is holding a value it has just proved untrustworthy. Records beyond that point may be perfectly intact and are simply **unaddressable**.

### What may be discarded

Truncating a segment throws away everything past its last valid record. Because offsets are derived by counting (Section 3), discarding records from the middle of a log does not leave a gap — it silently shifts every surviving record after that point down to a lower offset. Code that recorded "offset 7" before a crash would afterwards read a different record under that name, with no error reported anywhere.

So the rule is:

> **Truncation is safe if and only if no valid records exist after the truncation point.**

Damage at the tail satisfies this trivially — nothing follows it, so nothing can be renumbered. That is exactly what an interrupted write looks like, and why ordinary crash recovery is legitimate.

Damage anywhere else does not. In that case `NewLog` returns `ErrCorrupt` and **leaves every file untouched**, so the segments can be inspected or restored from a replica. Refusing to open is the correct outcome: losing availability is recoverable, and silently serving a different history is not.

`ErrCorrupt` is a sentinel wrapped with `%w`, so a caller can use `errors.Is` to tell disk corruption apart from a permissions error or a full disk — which matters to a replica deciding whether it needs a full copy from its primary.

---

## 6. Durability

Three modes, all measured on an Apple M4 Pro (macOS 26.6, APFS, Go 1.26.3). Full numbers in the [README](README.md#performance).

| Mode | An acknowledged write means | p50 (1 KB) | p99 (1 KB) |
|---|---|---|---|
| `SyncNone` | it is in the OS page cache | 334 ns | 9.96 µs |
| `SyncBatched` | it is on disk, or will be within N records or T ms | 500 ns | 3.49 ms |
| `SyncPerWrite` | it is on stable storage, now | 3.99 ms | 5.02 ms |

Two things are worth reading off that table.

**The gap is four orders of magnitude**, and it is entirely one syscall. Data transfer is not the cost; the `fsync` is.

**`SyncBatched`'s p99 is ~7000× its p50.** That is not noise, it is the shape of the design: most appends land in a buffer and return in half a microsecond, and then one append per batch pays the whole `fsync`. Batching does not remove the cost, it concentrates it on an unlucky caller. Any batching scheme has this shape, including the request batching discussed in Section 7.

**Platform note.** Go's `File.Sync()` on macOS issues `fcntl(F_FULLFSYNC)`, which forces a full drive cache flush to stable media. Linux's `fdatasync` typically returns in 50–500 µs on NVMe because it is permitted to leave data in the drive's volatile write cache. Same line of Go, an order of magnitude apart, and different guarantees. The ~4 ms figure here is the strong one.

### Two things durability needs beyond writing bytes

**Directory entries are durable separately from file contents.** Calling `Sync` on a segment guarantees its bytes survive a crash; it says nothing about whether the directory still lists the file. Without an `fsync` on the directory after rotation, a crash can leave a segment whose data reached the disk while its *name* did not — unreachable despite nothing being lost. Every mode except `SyncNone` pays for this.

**`Close` is part of the contract.** Flushing a `bufio.Writer` moves bytes into the page cache, which survives the process exiting but not the machine losing power. Any mode that promised durability must reach stable storage on `Close`, or a clean shutdown can still lose records already acknowledged as durable.

### Fail-stop on sync failure

If the background sync goroutine's `fsync` fails, the log records the error and **`Append` refuses all subsequent writes.**

This is a deliberate choice against the conventional one, which is to log the error and carry on. The reasoning: `SyncBatched` is a promise, and once the `fsync` backing it fails, the promise cannot be kept. Continuing to hand back offsets would mean acknowledging records with no basis to believe they will survive.

The cost is availability — one transient failure blocks writes until the log is reopened, with no retry path. That is defensible for a single node and has consequences at cluster level: a replica that fail-stops removes itself from any quorum. Reads are still allowed, because reading makes no durability claim and draining data off a dying disk is exactly when you want them to work.

---

## 7. Replication

One configured primary, one follower. gRPC, one unary RPC:

```proto
service Replication {
  rpc AppendEntries(AppendEntriesRequest) returns (AppendEntriesResponse);
}

message AppendEntriesRequest  { repeated bytes entries = 1; }
message AppendEntriesResponse { bool accepted = 1; }
```

The primary is the gRPC **client** and the follower the **server**. Client and server describe who opens the connection, not who has authority: the primary dials precisely *because* it is the one holding new records and deciding when they go out. If followers polled, the primary could not control when a write becomes replicated.

### Order of operations

`Put` appends to the primary's own log **first**, then replicates.

A record's offset does not exist until it is appended, so there is nothing coherent to send beforehand. More importantly, the reverse order would let a failed local append leave the follower holding a record the primary does not have — the follower would be *ahead* of the primary, which inverts what "primary" means.

Local-first is what makes this true:

> **The follower's log is always a prefix of the primary's log.**

A follower may lag. It must never contain something the primary is missing. This is the property that makes catch-up possible at all: a lagging follower needs records appended, not reconciled.

### Cost

Both nodes run `SyncPerWrite`, so an `accepted` response is an honest claim about stable storage. End-to-end that costs **6.5–8.6 ms per record** — the primary's `fsync`, the follower's `fsync`, and a round trip. Against a measured 3.99 ms `fsync`, the two syncs account for essentially all of it; **the network does not appear in the measurement.**

That is why records are sent one per RPC despite `entries` being a list. Batching amortises per-round-trip cost, and per-round-trip cost is not currently what is being paid. It becomes worth doing when the follower stops paying an `fsync` per entry, or when the two nodes are not on the same machine — and those two decisions have to be made together, because batching the wire moves the cost onto the follower's disk rather than removing it.

---

## 8. What fails

Everything here is a known limitation of the current stage, not a defect to be reported. Each is scheduled.

**The logs diverge silently and permanently.** If replication fails, the record stays in the primary's log and nowhere else. Nothing detects the gap and nothing reconciles it: the next write simply appends after what is already there, and the follower is never asked about what it missed. Restart a follower against an empty directory and it stays behind forever while every subsequent RPC returns `accepted: true`. This is asserted by a passing test — `TestPutDivergesWhenFollowerUnreachable` — written as an executable assertion rather than a comment, so that it *fails* once catch-up exists.

**A batch can be applied partially.** The follower appends entries in a loop. If the fourth of five fails, the first three are already durably on disk, and this is an append-only log — there is no undo. The primary concludes the whole batch failed. Neither side can discover the discrepancy, because `bool accepted` cannot express "I took three of your five." The response message is too small to describe the state the follower is actually in.

**Duplicates are stored.** Nothing identifies a request. A primary that retries after a timeout gets the record stored twice, with `accepted: true` both times.

**Concurrent requests could interleave.** gRPC runs every call on its own goroutine. The log's mutex guarantees no corruption, but it says nothing about ordering — two concurrent batches could interleave on disk. Ordering is the one thing a log is for, so thread-safety is not sufficient here. It cannot currently happen because the single primary sends one batch at a time and waits, which is a property of the caller's behaviour rather than a guarantee.

**No quorum, no elections, no failure detection.** One follower, and the primary reports success only if that follower accepts. Two nodes are less available than one. Promoting a follower by hand while the original primary still runs produces two divergent logs with no way to reconcile them.

**Startup is O(total bytes).** Section 3.

---

## 9. What's next

In order, and for the reason each is next rather than by feature appeal:

1. **N followers and quorum acknowledgement.** Commit once ⌈(N+1)/2⌉ nodes ack. The first genuinely distributed idea here, and it makes the system *more* available than a single node rather than less.
2. **Log matching and catch-up.** Requires the request to carry the position it expects the follower to be at, and the response to say where the follower actually is. This is the fix for both the divergence and the partial-batch problem in Section 8 — both are symptoms of a response too small to describe the follower's state.
3. **Failure injection.** Slow follower, erroring follower, death mid-append, network delay. Timeouts, retries, idempotency, backpressure.
4. **A split-brain demonstration.** Promote a follower by hand while the primary lives, write to both, document the divergence.

Then Raft, having met the problems it solves.
