// Command primary sends one hardcoded entry to a follower and reports the
// answer.
//
// It is a one-shot: it dials, sends, prints, exits. Fanning out to several
// followers and holding connections open is week 3.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/NiranjanBhosale/logstore/internal/replication"
)

func main() {
	peer := flag.String("peer", "localhost:50051", "follower address to send to")
	flag.Parse()

	// Background is the empty root context: no deadline, never cancelled.
	// Request derives its own 5 second timeout from it. A real primary would
	// pass something cancellable here so a shutdown could interrupt in-flight
	// calls, but nothing can interrupt this program yet.
	accepted, err := replication.Request(context.Background(), *peer)
	if err != nil {
		log.Fatalf("primary: %v", err)
	}

	log.Printf("follower %s replied accepted=%v", *peer, accepted)
}
