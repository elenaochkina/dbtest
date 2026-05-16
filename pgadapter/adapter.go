package pgadapter

import (
	"context"
	"fmt"
	"time"

	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pgxpool connection pool to any PostgreSQL-compatible
// database (Postgres, Aurora, RDS, etc.) using the given DSN.
// Pass a non-nil tel to emit a connection duration metric and log line;
// pass nil to connect without any observability.
// Example DSN: "postgres://postgres:test@localhost/postgres"
func Connect(dsn string, tel *telemetry.Telemetry) (*pgxpool.Pool, error) {
	start := time.Now()

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	if tel != nil {
		duration := time.Since(start).Seconds()
		tel.Metrics.ConnectionDuration.Observe(duration)
		tel.Logger.With("package", "pgadapter").Info("connected to database", "latency_seconds", duration)
	}

	return pool, nil
}
