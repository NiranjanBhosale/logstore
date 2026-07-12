package log

import "time"

// SyncMode controls when the log calls file.Sync() to flush data to disk.
type SyncMode int

const (
	// iota ensures that each constant gets the next integer.
	// SyncNone never calls file.Sync(). Fastest, least durable.
	// Data may be lost if the process or machine crashes.
	SyncNone SyncMode = iota

	// SyncPerWrite calls file.Sync() after every Append.
	// Slowest, but every acknowledged write is on stable storage.
	SyncPerWrite

	// SyncBatched calls file.Sync() periodically based on
	// record count or time interval, whichever comes first.
	SyncBatched
)

// SyncConfig holds configuration for the sync behavior.
type SyncConfig struct {
	Mode SyncMode

	// BatchRecords is the number of records between syncs (SyncBatched only).
	BatchRecords int

	// BatchInterval is the max time between syncs (SyncBatched only).
	BatchInterval time.Duration
}
