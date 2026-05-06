package adapter

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Option is a functional option for Connect.
type Option func(*options)

// options holds optional parameters for Connect.
type options struct {
	tel *telemetry.Telemetry // nil means no metrics
}

// WithMetrics attaches telemetry to the adapter.
// When provided, Connect will emit a duration metric and a log line.
func WithMetrics(tel *telemetry.Telemetry) Option {
	return func(o *options) { o.tel = tel }
}

// Connect opens a connection pool to Postgres using the given DSN.
// Example DSN: "postgres://postgres:test@localhost/postgres"
func Connect(dsn string, opts ...Option) (*pgxpool.Pool, error) {
	// apply options
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}

	// start timer before connecting
	start := time.Now()

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	// Ping to verify the connection actually works.
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	// calculate how long the connect took
	duration := time.Since(start).Seconds()

	// emit metric and log line only if telemetry was provided
	if o.tel != nil {
		o.tel.AdapterConnectDuration.Observe(duration)
		slog.Info("connected to database",
			"latency_seconds", duration,
		)
	}

	return pool, nil
}
