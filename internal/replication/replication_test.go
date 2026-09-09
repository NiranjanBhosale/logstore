package replication

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// startFollower opens a follower over a temporary directory and serves it on a
// port chosen by the operating system.
//
// Binding 127.0.0.1:0 and reading the address back from the listener is what
// keeps these tests from colliding with whatever else happens to be running.
// It is only possible because Serve accepts a listener the caller has already
// bound, rather than an address string it binds itself.
//
// The returned shutdown function cancels the server, waits for Serve to
// return, and closes the log, failing the test if any of those report an
// error. It is safe to call more than once.
func startFollower(t *testing.T) (f *Follower, dir, addr string, shutdown func()) {
	t.Helper()

	dir = t.TempDir()

	f, err := NewFollower(dir)
	if err != nil {
		t.Fatalf("NewFollower(%s): %v", dir, err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.Close()
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Serve blocks, so it runs on its own goroutine. Its error comes back over
	// a buffered channel: buffered so that the goroutine can finish even if the
	// test fails before reading from it, which would otherwise leak it.
	serveErr := make(chan error, 1)
	go func() { serveErr <- f.Serve(ctx, lis) }()

	var done bool
	shutdown = func() {
		if done {
			return
		}
		done = true

		cancel()
		if err := <-serveErr; err != nil {
			t.Errorf("Serve: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Errorf("Follower.Close: %v", err)
		}
	}

	return f, dir, lis.Addr().String(), shutdown
}

// segmentFiles reads every segment file in dir, keyed by filename.
func segmentFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}

	out := make(map[string][]byte)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = b
	}
	return out
}

// sortedKeys returns m's keys in order, so failure messages are stable.
func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestPutReplicatesToFollower is the week 2 milestone: records written through
// the primary end up byte for byte identical on both nodes.
//
// Comparing raw bytes rather than record counts is deliberate. Both ends run
// the same encoder, so identical files prove the payloads, their order, their
// lengths and their checksums all agree. A count would pass even if the
// follower had stored different data.
func TestPutReplicatesToFollower(t *testing.T) {
	f, followerDir, addr, shutdown := startFollower(t)
	defer shutdown()

	primaryDir := t.TempDir()
	p, err := NewPrimary(primaryDir, []string{addr})
	if err != nil {
		t.Fatalf("NewPrimary: %v", err)
	}

	const n = 20
	for i := 0; i < n; i++ {
		if err := p.Put(context.Background(), fmt.Appendf(nil, "record-%d", i)); err != nil {
			t.Fatalf("Put record %d: %v", i, err)
		}
	}

	if got := p.primaryLog.NumRecords(); got != n {
		t.Errorf("primary records: got %d, want %d", got, n)
	}
	if got := f.followerLog.NumRecords(); got != n {
		t.Errorf("follower records: got %d, want %d", got, n)
	}

	// Both logs have to be closed before their files are compared. Buffered
	// writes are not on disk until then, so reading the files while the logs
	// are open would compare whatever each side happened to have flushed.
	if err := p.Close(); err != nil {
		t.Fatalf("Primary.Close: %v", err)
	}
	shutdown()

	primaryFiles := segmentFiles(t, primaryDir)
	followerFiles := segmentFiles(t, followerDir)

	if len(primaryFiles) == 0 {
		t.Fatal("primary wrote no segment files")
	}
	if pk, fk := sortedKeys(primaryFiles), sortedKeys(followerFiles); len(pk) != len(fk) {
		t.Fatalf("segment files differ: primary %v, follower %v", pk, fk)
	}

	for _, name := range sortedKeys(primaryFiles) {
		want, got := primaryFiles[name], followerFiles[name]
		if got == nil {
			t.Errorf("%s: missing on follower", name)
			continue
		}
		if !bytes.Equal(want, got) {
			t.Errorf("%s: differs (primary %d bytes, follower %d bytes)",
				name, len(want), len(got))
		}
	}
}

// TestPutDivergesWhenFollowerUnreachable pins down what currently happens when
// replication fails, which is not a bug so much as the limit of what this
// protocol can express.
//
// Put appends locally before it replicates, so a failed send leaves the record
// in the primary's log and nowhere else. The primary is now permanently ahead,
// and nothing in the system will ever notice or reconcile it: the next Put
// simply appends after what is already there, and the follower is never asked
// about the records it missed.
//
// When catch-up exists this test should start failing, and the assertion that
// the follower is short becomes the thing that has to change.
func TestPutDivergesWhenFollowerUnreachable(t *testing.T) {
	f, _, addr, shutdown := startFollower(t)
	defer shutdown()

	primaryDir := t.TempDir()
	p, err := NewPrimary(primaryDir, []string{addr})
	if err != nil {
		t.Fatalf("NewPrimary: %v", err)
	}
	defer p.Close()

	if err := p.Put(context.Background(), []byte("delivered")); err != nil {
		t.Fatalf("Put while follower is up: %v", err)
	}

	// Take the follower away mid-stream.
	shutdown()

	// Bound the wait: Put derives its timeout from this context, and the
	// earlier of the two deadlines wins, so a refused connection cannot stall
	// the test for the full five seconds.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := p.Put(ctx, []byte("lost")); err == nil {
		t.Fatal("Put succeeded with the follower down; expected a replication error")
	}

	// The record the primary failed to replicate is still in its own log.
	if got := p.primaryLog.NumRecords(); got != 2 {
		t.Errorf("primary records: got %d, want 2", got)
	}
	if got := f.followerLog.NumRecords(); got != 1 {
		t.Errorf("follower records: got %d, want 1 (it never saw the second)", got)
	}
}
