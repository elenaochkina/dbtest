// Command probe measures database availability at two levels and checks that
// acknowledged commits survive a disruption.
//
// Each sample opens one connection and takes both observations from it:
// "readable" (the server returned the counter) and "writable" (the server acked
// an advance of it).
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
	flag.DurationVar(&cfg.Interval, "interval", 250*time.Millisecond, "time between samples")
	// Must stay under a second: a connect to a host that has gone away is not
	// refused, it is dropped, and Linux does not retry the SYN for a full second.
	flag.DurationVar(&cfg.Timeout, "timeout", 100*time.Millisecond, "per-sample connect + read timeout")
	flag.DurationVar(&cfg.WriteTimeout, "write-timeout", 500*time.Millisecond, "per-sample write timeout")
	flag.DurationVar(&cfg.MaxDuration, "max-duration", time.Hour, "stop polling even if nothing stops the probe")
	flag.Parse()

	if cfg.DSN == "" {
		slog.Error("-dsn is required")
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
	// The probe reports what it saw and exits. Whether that matches the
	// disruptions applied is for the caller that applied them to judge.
	fmt.Println(string(payload))
}
