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

// testFollower is a follower running in this process, with the directory it
// writes to and the address it is reachable on.
type testFollower struct {
	*Follower
	dir      string
	addr     string
	shutdown func()
}

// startFollower opens a follower over a temporary directory and serves it on a
// port chosen by the operating system.
//
// Binding 127.0.0.1:0 and reading the address back from the listener is what
// keeps these tests from colliding with whatever else happens to be running,
// and from depending on how localhost resolves. It is only possible because
// Serve accepts a listener the caller has already bound, rather than an
// address string it binds itself.
//
// shutdown cancels the server, waits for Serve to return, and closes the log,
// failing the test if any of those report an error. It is registered with
// t.Cleanup so a follower cannot outlive its test, and it is safe to call
// again by hand, which is how a test takes a follower away mid-run.
func startFollower(t *testing.T) *testFollower {
	t.Helper()

	dir := t.TempDir()

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
	shutdown := func() {
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
	t.Cleanup(shutdown)

	return &testFollower{
		Follower: f,
		dir:      dir,
		addr:     lis.Addr().String(),
		shutdown: shutdown,
	}
}

// startFollowers starts n independent followers, each with its own directory
// and its own port.
func startFollowers(t *testing.T, n int) []*testFollower {
	t.Helper()

	followers := make([]*testFollower, 0, n)
	for range n {
		followers = append(followers, startFollower(t))
	}
	return followers
}

// addrsOf returns the addresses of the given followers, ready to hand to
// NewPrimary.
func addrsOf(followers []*testFollower) []string {
	addrs := make([]string, 0, len(followers))
	for _, f := range followers {
		addrs = append(addrs, f.addr)
	}
	return addrs
}

// logBytes returns everything a node has written, as one byte slice: the
// segment files concatenated in name order, which is the order they were
// filled.
//
// Reading the raw bytes rather than the records is deliberate. Both ends run
// the same encoder, so identical bytes prove the payloads, their order, their
// lengths and their checksums all agree at once.
func logBytes(t *testing.T, dir string) []byte {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var out []byte
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out = append(out, b...)
	}
	return out
}

// TestQuorumSize pins the arithmetic down, because an off-by-one here produces
// a cluster that reports healthy and is not.
//
// The cluster is the followers plus the primary, a majority of n nodes is
// n/2+1, and the primary has already appended locally by the time quorum is
// consulted, so it supplies one of those acknowledgements itself.
//
// The row worth reading is two followers: a three node cluster needs the same
// single acknowledgement a two node cluster does, but can lose a follower and
// carry on. That is where a replica starts buying availability instead of
// costing it.
func TestQuorumSize(t *testing.T) {
	cases := []struct{ followers, want int }{
		{0, 0},
		{1, 1},
		{2, 1},
		{3, 2},
		{4, 2},
		{5, 3},
		{6, 3},
	}

	for _, c := range cases {
		p := &Primary{peers: make([]*Peer, c.followers)}
		if got := p.quorum(); got != c.want {
			t.Errorf("%d followers (%d node cluster): quorum() = %d, want %d",
				c.followers, c.followers+1, got, c.want)
		}
	}
}

// TestPutReplicatesToFollower is the week 2 milestone, and it uses exactly one
// follower on purpose.
//
// With a single follower the quorum is one, so Put cannot return before that
// follower has answered and no record can be left behind. That makes "both
// logs are identical" a safe assertion. It stops being safe as soon as there
// are enough followers for one of them to be a straggler, which is what
// TestPutReplicatesToAllFollowers has to deal with.
func TestPutReplicatesToFollower(t *testing.T) {
	f := startFollower(t)

	primaryDir := t.TempDir()
	p, err := NewPrimary(primaryDir, []string{f.addr})
	if err != nil {
		t.Fatalf("NewPrimary: %v", err)
	}

	const n = 20
	for i := range n {
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
	f.shutdown()

	want := logBytes(t, primaryDir)
	got := logBytes(t, f.dir)

	if len(want) == 0 {
		t.Fatal("primary wrote nothing")
	}
	if !bytes.Equal(want, got) {
		t.Errorf("logs differ: primary %d bytes, follower %d bytes", len(want), len(got))
	}
}

// TestPutReplicatesToAllFollowers checks what can actually be promised once a
// write commits on a majority rather than on everyone.
//
// With three followers the quorum is two, so Put returns as soon as the
// fastest two have answered and cancels the third. No individual follower is
// guaranteed to hold every record, and which one falls behind is a race, so
// asserting that all three match the primary would be asserting something the
// design does not promise. It would also pass almost every time on a quiet
// machine and fail in CI, which is worse than not testing it.
//
// What is guaranteed is that no follower ever holds anything the primary does
// not, because Put appends locally before it sends. That makes every
// follower's log a prefix of the primary's, and a prefix is exactly what
// catch-up is able to repair later.
func TestPutReplicatesToAllFollowers(t *testing.T) {
	followers := startFollowers(t, 3)

	primaryDir := t.TempDir()
	p, err := NewPrimary(primaryDir, addrsOf(followers))
	if err != nil {
		t.Fatalf("NewPrimary: %v", err)
	}

	const n = 20
	for i := range n {
		if err := p.Put(context.Background(), fmt.Appendf(nil, "record-%d", i)); err != nil {
			t.Fatalf("Put record %d: %v", i, err)
		}
	}

	if got := p.primaryLog.NumRecords(); got != n {
		t.Errorf("primary records: got %d, want %d", got, n)
	}

	if err := p.Close(); err != nil {
		t.Fatalf("Primary.Close: %v", err)
	}
	for _, f := range followers {
		f.shutdown()
	}

	primaryBytes := logBytes(t, primaryDir)
	if len(primaryBytes) == 0 {
		t.Fatal("primary wrote nothing")
	}

	for i, f := range followers {
		followerBytes := logBytes(t, f.dir)
		if !bytes.HasPrefix(primaryBytes, followerBytes) {
			t.Errorf("follower %d (%s) is not a prefix of the primary: "+
				"follower has %d bytes, primary %d",
				i, f.addr, len(followerBytes), len(primaryBytes))
		}
		t.Logf("follower %d holds %d of %d bytes", i, len(followerBytes), len(primaryBytes))
	}
}

// TestPutSurvivesMinorityFailure is the point of the whole week: a cluster
// keeps accepting writes after losing a follower.
//
// Three followers need two acknowledgements. Taking one away leaves exactly
// two, so writes continue. With a single follower the same failure would stop
// the cluster dead, which is why one replica is a liability and two are
// redundancy.
func TestPutSurvivesMinorityFailure(t *testing.T) {
	followers := startFollowers(t, 3)

	primaryDir := t.TempDir()
	p, err := NewPrimary(primaryDir, addrsOf(followers))
	if err != nil {
		t.Fatalf("NewPrimary: %v", err)
	}
	defer p.Close()

	if err := p.Put(context.Background(), []byte("before the failure")); err != nil {
		t.Fatalf("Put with all three followers up: %v", err)
	}

	followers[2].shutdown()

	// Bounded so a write that cannot reach quorum fails the test quickly
	// rather than sitting out Put's own five second timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for i := range 5 {
		if err := p.Put(ctx, fmt.Appendf(nil, "after-%d", i)); err != nil {
			t.Fatalf("Put %d with two of three followers up: %v", i, err)
		}
	}

	if got := p.primaryLog.NumRecords(); got != 6 {
		t.Errorf("primary records: got %d, want 6", got)
	}
}

// TestPutFailsWithoutQuorum is the other half of the rule. Once too few
// followers remain, the primary refuses the write rather than acknowledging a
// record a majority does not hold.
//
// The record is still in the primary's own log afterwards, because Put appends
// before it replicates. The primary is therefore ahead of every follower and
// nothing will reconcile that, which is the gap week 4 closes.
func TestPutFailsWithoutQuorum(t *testing.T) {
	followers := startFollowers(t, 3)

	primaryDir := t.TempDir()
	p, err := NewPrimary(primaryDir, addrsOf(followers))
	if err != nil {
		t.Fatalf("NewPrimary: %v", err)
	}
	defer p.Close()

	if err := p.Put(context.Background(), []byte("accepted")); err != nil {
		t.Fatalf("Put with all three followers up: %v", err)
	}

	// Two gone leaves one, short of the two needed.
	followers[1].shutdown()
	followers[2].shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = p.Put(ctx, []byte("rejected"))
	if err == nil {
		t.Fatal("Put succeeded with only one of three followers reachable")
	}
	if !strings.Contains(err.Error(), "needed 2") {
		t.Errorf("error should say how many acknowledgements were needed, got: %v", err)
	}
	t.Logf("refused with: %v", err)

	// The rejected record is in the primary's log regardless, so the primary is
	// now permanently ahead of every follower.
	if got := p.primaryLog.NumRecords(); got != 2 {
		t.Errorf("primary records: got %d, want 2", got)
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
	f := startFollower(t)

	primaryDir := t.TempDir()
	p, err := NewPrimary(primaryDir, []string{f.addr})
	if err != nil {
		t.Fatalf("NewPrimary: %v", err)
	}
	defer p.Close()

	if err := p.Put(context.Background(), []byte("delivered")); err != nil {
		t.Fatalf("Put while follower is up: %v", err)
	}

	// Take the follower away mid-stream.
	f.shutdown()

	// Bound the wait: Put derives its timeout from this context, and the
	// earlier of the two deadlines wins, so a refused connection cannot stall
	// the test for the full five seconds.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
