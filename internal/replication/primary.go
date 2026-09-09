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

	var (
		accepted int
		errs     []error
	)

	// One record per request for now. Entries is a list precisely so that a
	// single round trip can carry a batch, which is worth doing once there is a
	// measurement to justify it.
	for _, peer := range p.peers {
		resp, err := peer.client.AppendEntries(ctx, &replicationpb.AppendEntriesRequest{
			Entries: [][]byte{data},
		})

		// A failed RPC returns a nil resp, so nothing below may touch it. The
		// continue matters as much as recording the error: falling through
		// would dereference that nil.
		if err != nil {
			errs = append(errs, fmt.Errorf("replicate to %s: %w", peer.addr, err))
			continue
		}
		if !resp.GetAccepted() {
			errs = append(errs, fmt.Errorf("follower %s rejected the entry", peer.addr))
			continue
		}
		accepted++
	}

	if accepted < len(p.peers) {
		return fmt.Errorf("replicated to %d of %d followers: %w",
			accepted, len(p.peers), errors.Join(errs...))
	}
	return nil
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
