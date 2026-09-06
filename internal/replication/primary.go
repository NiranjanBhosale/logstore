package replication

import (
	"context"
	"fmt"
	"time"

	"github.com/NiranjanBhosale/logstore/internal/replication/replicationpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Request sends one hardcoded entry to the follower at addr and reports
// whether the follower accepted it.
//
// It returns the follower's answer rather than logging it, so the caller
// decides what to do with the result. Deciding is main's job, not a library's.
func Request(ctx context.Context, addr string) (bool, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false, fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	client := replicationpb.NewReplicationClient(conn)

	// Derived from the caller's ctx, not context.Background(), so cancelling
	// the parent cancels this call too. The deadline means a dead follower
	// cannot hang the primary indefinitely.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := client.AppendEntries(ctx, &replicationpb.AppendEntriesRequest{
		Entries: [][]byte{[]byte("hello from the primary")},
	})
	if err != nil {
		return false, fmt.Errorf("AppendEntries to %s: %w", addr, err)
	}

	return resp.GetAccepted(), nil
}
