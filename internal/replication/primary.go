package replication

import (
	"context"
	"errors"
	"fmt"
	"time"

	logstore "github.com/NiranjanBhosale/logstore/internal/log"
	"github.com/NiranjanBhosale/logstore/internal/replication/replicationpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Primary struct {
	primaryLog *logstore.Log
	conn       *grpc.ClientConn
	client     replicationpb.ReplicationClient
	peer       string
}

func NewPrimary(dir, peerAddr string) (*Primary, error) {
	primaryConn, err := grpc.NewClient(peerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", peerAddr, err)
	}
	newPrimaryLog, err := logstore.NewLog(dir, 0, logstore.SyncConfig{Mode: logstore.SyncPerWrite})
	if err != nil {
		primaryConn.Close() // release what we already hold
		return nil, fmt.Errorf("open log at %s: %w", dir, err)
	}

	return &Primary{
		primaryLog: newPrimaryLog,
		conn:       primaryConn,
		client:     replicationpb.NewReplicationClient(primaryConn),
		peer:       peerAddr,
	}, nil
}

// Put appends data to the primary's own log and then replicates it to the
// follower.
//
// The local append happens first. The primary's log defines the order of
// records, so a record must have a place in that sequence before it can be
// sent anywhere. Replicating first would mean a failed local append left the
// follower holding a record the primary does not have, inverting the
// relationship between them: a follower may lag, but it must never hold
// anything the primary is missing.
func (p *Primary) Put(ctx context.Context, data []byte) error {
	if _, err := p.primaryLog.Append(data); err != nil {
		return fmt.Errorf("append locally: %w", err)
	}

	// A follower that accepts the connection and then stops responding would
	// otherwise hang this call forever, which is worse than an outright
	// failure because nothing reports it.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// One record per request for now. Entries is a list precisely so that a
	// single round trip can carry a batch, which is worth doing once there is a
	// measurement to justify it.
	resp, err := p.client.AppendEntries(ctx, &replicationpb.AppendEntriesRequest{
		Entries: [][]byte{data},
	})

	// err must be checked before resp is touched: a failed RPC returns a nil
	// resp, and reading a field off it would panic rather than report the
	// failure.
	if err != nil {
		return fmt.Errorf("replicate to %s: %w", p.peer, err)
	}
	if !resp.GetAccepted() {
		return fmt.Errorf("follower %s rejected the entry", p.peer)
	}

	return nil
}

func (p *Primary) Close() error {
	logErr := p.primaryLog.Close()
	connErr := p.conn.Close()
	return errors.Join(logErr, connErr)
}
