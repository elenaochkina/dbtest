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
	"github.com/jackc/pgx/v5"
)

func main() {
	var cfg probe.Config
	flag.StringVar(&cfg.DSN, "dsn", "", "target database DSN (required)")
	flag.DurationVar(&cfg.Interval, "interval", 250*time.Millisecond, "time between samples")
	// Must stay under a second: a connect to a host that has gone away is not
	// refused, it is dropped, and Linux does not retry the SYN for a full second.
	flag.DurationVar(&cfg.Timeout, "timeout", 300*time.Millisecond, "per-sample connect + read timeout")
	flag.DurationVar(&cfg.WriteTimeout, "write-timeout", 200*time.Millisecond, "per-sample write timeout")
	flag.DurationVar(&cfg.MaxDuration, "max-duration", time.Hour, "stop polling even if nothing stops the probe")
	ready := flag.Duration("ready", 0, "connect once a second until the database answers or this long passes, then exit")
	sampling := flag.Duration("sampling", 0, "read the probe counter once a second until it is above 0 or this long passes, then exit")
	flag.Parse()

	if cfg.DSN == "" {
		slog.Error("-dsn is required")
		os.Exit(2)
	}

	// SIGTERM is how a container is asked to stop;
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *ready > 0 {
		os.Exit(waitFor(ctx, cfg.DSN, *ready, "SELECT true", "database answered"))
	}
	if *sampling > 0 {
		os.Exit(waitFor(ctx, cfg.DSN, *sampling, "SELECT seq > 0 FROM dbtest_probe WHERE id = 1", "probe is sampling"))
	}

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

// waitFor connects and runs query once a second until it returns true or the
// limit passes. It returns 0 when the query returned true and 1 when it did not.
func waitFor(ctx context.Context, dsn string, limit time.Duration, query, done string) int {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	for {
		attempt, cancelAttempt := context.WithTimeout(ctx, 5*time.Second)
		ok := false
		conn, err := pgx.Connect(attempt, dsn)
		if err == nil {
			err = conn.QueryRow(attempt, query).Scan(&ok)
			conn.Close(context.Background())
		}
		cancelAttempt()
		if err == nil && ok {
			slog.Info(done)
			return 0
		}
		select {
		case <-ctx.Done():
			slog.Error("gave up", "query", query, "within", limit, "error", err)
			return 1
		case <-time.After(time.Second):
		}
	}
}
