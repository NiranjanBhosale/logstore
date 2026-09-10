package replication

import (
	"context"
	"errors"
	"fmt"
	"time"

	logstore "github.com/NiranjanBhosale/logstore/internal/log"
	"github.com/NiranjanBhosale/logstore/internal/replication/replicationpb"
)

type Primary struct {
	primaryLog *logstore.Log
	peers      []*Peer
}

// NewPrimary opens the primary's log in dir and prepares a connection to each
// address in peerAddrs.
//
// A failure here is a configuration problem, not an unreachable follower:
// grpc.NewClient does not connect, so a peer whose follower is down still
// builds successfully. Only a malformed address fails, and that is reported
// rather than skipped, because silently starting with fewer peers than were
// asked for would change the cluster's fault tolerance without saying so.
//
// If any step fails, everything already acquired is released before returning,
// so a failed call leaves nothing open.
func NewPrimary(dir string, peerAddrs []string) (*Primary, error) {
	primaryLog, err := logstore.NewLog(dir, 0, logstore.SyncConfig{Mode: logstore.SyncPerWrite})
	if err != nil {
		return nil, fmt.Errorf("open log at %s: %w", dir, err)
	}

	peers := make([]*Peer, 0, len(peerAddrs))
	for _, addr := range peerAddrs {
		peer, err := NewPeer(addr)
		if err != nil {
			// Unwind: release the peers built so far, then the log. How much
			// there is to clean up depends on how far the loop got.
			for _, built := range peers {
				built.Close()
			}
			primaryLog.Close()
			return nil, fmt.Errorf("create peer %s: %w", addr, err)
		}
		peers = append(peers, peer)
	}

	return &Primary{
		primaryLog: primaryLog,
		peers:      peers,
	}, nil
}

// Put appends data to the primary's own log and then replicates it to every
// peer.
//
// The local append happens first. The primary's log defines the order of
// records, so a record must have a place in that sequence before it can be
// sent anywhere. Replicating first would mean a failed local append left a
// follower holding a record the primary does not have, inverting the
// relationship between them: a follower may lag, but it must never hold
// anything the primary is missing.
//
// Every peer is contacted before a verdict is reached, rather than returning
// on the first failure. Deciding whether a write succeeded is a question about
// how many followers accepted it, which cannot be answered until they have all
// been heard from. For now the rule is that all of them must accept; session 2
// replaces that with a majority and sends to the peers concurrently.
func (p *Primary) Put(ctx context.Context, data []byte) error {
	if _, err := p.primaryLog.Append(data); err != nil {
		return fmt.Errorf("append locally: %w", err)
	}

	// A follower that accepts the connection and then stops responding would
	// otherwise hang this call forever, which is worse than an outright
	// failure because nothing reports it.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// The channel is buffered to hold every peer's result. That is not an
	// optimisation: the loop below stops reading once a majority has answered,
	// and on an unbuffered channel the goroutines still in flight would block
	// forever trying to hand over a result nobody will take. One leaked
	// goroutine per slow follower, on every write.
	results := make(chan replicaResult, len(p.peers))

	// One record per request for now. Entries is a list precisely so that a
	// single round trip can carry a batch, which is worth doing once there is a
	// measurement to justify it.
	for _, peer := range p.peers {
		go func() {
			resp, err := peer.client.AppendEntries(ctx, &replicationpb.AppendEntriesRequest{
				Entries: [][]byte{data},
			})

			// A failed RPC returns a nil resp, so nothing below may touch it.
			switch {
			case err != nil:
				results <- replicaResult{err: fmt.Errorf("replicate to %s: %w", peer.addr, err)}
			case !resp.GetAccepted():
				results <- replicaResult{err: fmt.Errorf("follower %s rejected the entry", peer.addr)}
			default:
				results <- replicaResult{}
			}
		}()
	}

	needed := p.quorum()

	var (
		accepted int
		errs     []error
	)

	// Zero followers is a one-node cluster, where the primary's own append is
	// already a majority. The loop below would return success anyway, but only
	// by never running; saying so here keeps that from looking accidental.
	for range p.peers {
		r := <-results
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}

		accepted++
		if accepted >= needed {
			// Return on the fastest majority rather than waiting for the
			// slowest follower. The peers that have not answered yet are still
			// mid-RPC, and the deferred cancel above fires as this returns, so
			// their calls are cancelled.
			//
			// Their handlers do not check ctx, so a cancelled follower still
			// finishes appending and only loses the reply. It therefore holds
			// the record while the primary has no idea that it does. The
			// prefix invariant survives, since the primary appended first and
			// no follower can be ahead of it; what is lost is knowledge, not
			// agreement. Closing that gap is what the position fields in
			// week 4 are for.
			return nil
		}
	}

	return fmt.Errorf("replicated to %d of %d followers, needed %d: %w",
		accepted, len(p.peers), needed, errors.Join(errs...))
}

// quorum returns the number of follower acknowledgements a write needs before
// it is committed.
//
// The cluster is the followers plus the primary, and a majority of n nodes is
// n/2+1. The primary has already appended locally by the time this is
// consulted, so it contributes one of those acknowledgements itself and the
// followers only have to supply the rest.
//
// The interesting row is two followers: a three node cluster still needs only
// one follower acknowledgement, exactly like a two node cluster, but it can
// now lose a follower and keep accepting writes. That is where replication
// starts buying availability rather than costing it.
func (p *Primary) quorum() int {
	return (len(p.peers) + 1) / 2
}

// replicaResult is one follower's answer to a replication attempt. A nil err
// means the follower accepted the entry.
type replicaResult struct {
	err error
}

// Close closes the log and every peer connection.
//
// Every close is attempted regardless of earlier failures: a peer that fails
// to close must not prevent the log from being flushed and fsynced, and a
// failed log close must not leave connections open. All the errors are
// reported together.
func (p *Primary) Close() error {
	errs := make([]error, 0, len(p.peers)+1)

	errs = append(errs, p.primaryLog.Close())
	for _, peer := range p.peers {
		errs = append(errs, peer.Close())
	}

	return errors.Join(errs...)
}
