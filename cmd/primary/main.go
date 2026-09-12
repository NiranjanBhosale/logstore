// Command primary appends records to its own log and replicates each one to a
// follower.
//
// It sends a fixed number of records and exits. Fanning out to several
// followers is week 3.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/NiranjanBhosale/logstore/internal/replication"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() (err error) {
	peers := flag.String("peers", "localhost:50051", "follower address to send to")
	dir := flag.String("dir", "primary-data", "directory to store the primary's log")
	count := flag.Int("count", 10, "number of records to send")
	flag.Parse()

	// Unlike the follower, this program is not waiting to be shut down: it has
	// a finite amount of work. The signal context is here so that Ctrl-C part
	// way through a long run cancels the in-flight call and lets the deferred
	// Close below flush and fsync, rather than killing the process mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p, err := replication.NewPrimary(*dir, strings.Split(*peers, ","))
	if err != nil {
		return fmt.Errorf("create primary: %w", err)
	}

	// Close closes both the log and the connection, and either can fail. A bare
	// defer p.Close() would discard that. Assigning to the named return reports
	// it instead, unless the run already failed for a more informative reason.
	defer func() {
		if cerr := p.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close primary: %w", cerr)
		}
	}()

	for i := 0; i < *count; i++ {
		data := fmt.Appendf(nil, "record-%d", i)
		if err := p.Put(ctx, data); err != nil {
			return fmt.Errorf("put record %d of %d: %w", i, *count, err)
		}
	}

	log.Printf("replicated %d records to %s", *count, *peers)
	return nil
}
