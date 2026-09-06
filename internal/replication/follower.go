package replication

import (
	"context"
	"fmt"
	"log"
	"net"

	"github.com/NiranjanBhosale/logstore/internal/replication/replicationpb"
	"google.golang.org/grpc"
)

type Follower struct {
	replicationpb.UnimplementedReplicationServer
}

func (f *Follower) AppendEntries(ctx context.Context, req *replicationpb.AppendEntriesRequest) (*replicationpb.AppendEntriesResponse, error) {
	// GetEntries returns an empty slice rather than panicking when the request
	// is nil, so it is always safer than reaching into req.Entries directly.
	entries := req.GetEntries()
	log.Printf("received %d entries", len(entries))

	// repeated in the proto permits zero entries, so a caller is within its
	// rights to send none. Indexing without this check would panic, and a
	// panic in a gRPC handler takes down the whole follower process.
	if len(entries) > 0 {
		log.Printf("first entry: %s", entries[0])
	}

	return &replicationpb.AppendEntriesResponse{Accepted: true}, nil
}

func (f *Follower) Serve(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	srv := grpc.NewServer()
	replicationpb.RegisterReplicationServer(srv, f)

	log.Printf("follower listening on %s", addr)

	// Serve blocks until the server stops, then reports why.
	return srv.Serve(lis)
}
