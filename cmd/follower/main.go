// Command follower runs a single replication follower.
//
// It listens for AppendEntries calls and logs what arrives. It does not yet
// store anything: wiring the follower to its own logstore is week 2.
package main

import (
	"flag"
	"log"

	"github.com/NiranjanBhosale/logstore/internal/replication"
)

func main() {
	addr := flag.String("addr", ":50051", "address to listen on")
	flag.Parse()

	f := &replication.Follower{}

	// Serve blocks for as long as the server is healthy, so reaching the next
	// line at all means something went wrong. main is the right place to give
	// up: it owns the process, which is why log.Fatalf belongs here and not
	// inside the replication package.
	if err := f.Serve(*addr); err != nil {
		log.Fatalf("follower: %v", err)
	}
}
