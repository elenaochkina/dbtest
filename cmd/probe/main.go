// Command probe measures database availability at two levels and checks that
// acknowledged commits survive a disruption.
//
// Each sample opens one connection and takes both observations from it:
// "reachable" (the server answered a trivial query) and "writable" (the server
// acked an insert).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/elenaochkina/dbtest/probe"
)

func main() {
	var cfg probe.Config
	flag.StringVar(&cfg.DSN, "dsn", "", "target database DSN (required)")
	flag.DurationVar(&cfg.Interval, "interval", 20*time.Millisecond, "time between samples")
	flag.DurationVar(&cfg.Timeout, "timeout", 2*time.Second, "per-sample connect + read timeout")
	flag.DurationVar(&cfg.WriteTimeout, "write-timeout", 500*time.Millisecond, "per-sample write timeout")
	flag.DurationVar(&cfg.MaxDuration, "max-duration", 10*time.Minute, "give up if the expected outages never arrive")
	flag.IntVar(&cfg.Repetitions, "repetitions", 1, "how many disruptions to observe before exiting")
	flag.Parse()

	if cfg.DSN == "" {
		slog.Error("-dsn is required")
		os.Exit(2)
	}
	if cfg.Repetitions < 1 {
		slog.Error("-repetitions must be at least 1")
		os.Exit(2)
	}

	// SIGTERM is how a container is asked to stop;
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := probe.New(cfg)

	if err := p.Prepare(ctx); err != nil {
		slog.Error("prepare probe table", "error", err)
		os.Exit(1)
	}

	res := p.Run(ctx)

	payload, err := json.Marshal(res)
	if err != nil {
		slog.Error("marshal result", "error", err)
		os.Exit(1)
	}
	fmt.Println(string(payload))

	// A measurement that never saw what it was sent to see is a failure of the
	// prober's job. Lost commits are not — that is a successful measurement of a
	// bad outcome, and the caller decides what it means.
	if res.Error != "" {
		slog.Error("measurement incomplete", "error", res.Error)
		os.Exit(1)
	}
}
