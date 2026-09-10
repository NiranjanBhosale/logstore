// Command follower runs a single replication follower.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/NiranjanBhosale/logstore/internal/replication"
)

func main() {
	// main does nothing but decide that a failure is fatal. The work happens in
	// run so that run's deferred cleanup executes before the process exits:
	// os.Exit, which log.Fatal calls, does not run defers.
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() (err error) {
	addr := flag.String("addr", ":50051", "address to listen on")
	dir := flag.String("dir", "follower-data", "directory to store the follower's log")
	flag.Parse()

	// NotifyContext returns a context that is cancelled when one of these
	// signals arrives: Ctrl-C sends SIGINT, and SIGTERM is what most process
	// supervisors send. stop restores the default behaviour, so a second Ctrl-C
	// kills the process outright if the graceful path ever hangs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	f, err := replication.NewFollower(*dir)
	if err != nil {
		return fmt.Errorf("create follower: %w", err)
	}

	// Close flushes the buffer and fsyncs, and either can fail. A bare
	// defer f.Close() would throw that error away. Assigning to the named
	// return reports it instead -- unless Serve already failed, in which case
	// that error is the more informative one and should win.
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close follower: %w", cerr)
		}
	}()

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *addr, err)
	}

	// Serve logs the bound address itself, so there is nothing to announce here.
	return f.Serve(ctx, lis)
}
