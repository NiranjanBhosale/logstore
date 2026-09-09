package replication

import (
	"fmt"

	"github.com/NiranjanBhosale/logstore/internal/replication/replicationpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Peer struct {
	addr   string
	conn   *grpc.ClientConn
	client replicationpb.ReplicationClient
}

func NewPeer(addr string) (*Peer, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	return &Peer{
		addr:   addr,
		conn:   conn,
		client: replicationpb.NewReplicationClient(conn),
	}, nil
}

func (p *Peer) Close() error {
	if err := p.conn.Close(); err != nil {
		return fmt.Errorf("close connection to %s: %w", p.addr, err)
	}
	return nil
}
