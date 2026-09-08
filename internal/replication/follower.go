package replication

import (
	"context"
	"log"
	"net"

	logstore "github.com/NiranjanBhosale/logstore/internal/log"
	"github.com/NiranjanBhosale/logstore/internal/replication/replicationpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Follower struct {
	replicationpb.UnimplementedReplicationServer
	followerLog *logstore.Log
}

func NewFollower(dir string) (*Follower, error) {

	newlog, err := logstore.NewLog(dir, 0, logstore.SyncConfig{Mode: logstore.SyncPerWrite})
	if err != nil {
		return nil, err
	}

	return &Follower{
		followerLog: newlog,
	}, nil
}

func (f *Follower) AppendEntries(ctx context.Context, req *replicationpb.AppendEntriesRequest) (*replicationpb.AppendEntriesResponse, error) {
	// GetEntries returns an empty slice rather than panicking when the request
	// is nil, so it is always safer than reaching into req.Entries directly.
	entries := req.GetEntries()

	for i, e := range entries {
		if _, err := f.followerLog.Append(e); err != nil {
			// the follower has durably stored some prefix of this batch; the primary will conclude
			// none of it landed; nothing in the protocol lets either side discover the discrepancy
			return nil, status.Errorf(codes.Internal,
				"append entry %d of %d: %v", i, len(entries), err)
		}
	}

	return &replicationpb.AppendEntriesResponse{Accepted: true}, nil
}

// Serve handles AppendEntries calls on lis until ctx is cancelled or the
// server fails. The caller owns lis and is responsible for creating it, which
// lets a test bind 127.0.0.1:0 and read back the port the OS chose.
//
// Cancelling ctx begins a graceful shutdown: the server stops accepting new
// calls, lets the handlers already running finish, and only then returns.
func (f *Follower) Serve(ctx context.Context, lis net.Listener) error {
	srv := grpc.NewServer()
	replicationpb.RegisterReplicationServer(srv, f)

	// lis.Addr reports the address actually bound, so a caller that asked for
	// port 0 learns which port the OS picked.
	log.Printf("follower listening on %s", lis.Addr())

	// Both waiting for cancellation and serving requests block forever, so one
	// of them has to run on its own goroutine. The watcher goes here because
	// srv.Serve is what this function reports the result of.
	go func() {
		<-ctx.Done()
		log.Printf("shutdown requested, waiting for in-flight calls")

		// GracefulStop refuses new calls and waits for handlers already running
		// to return. Stop would cut them off mid-execution, which once
		// AppendEntries writes to the log could leave entries durably stored
		// that the caller is never told about.
		srv.GracefulStop()
	}()

	// Serve returns nil once GracefulStop has finished, so a clean shutdown is
	// not reported as a failure.
	return srv.Serve(lis)
}

func (f *Follower) Close() error {
	return f.followerLog.Close()
}
