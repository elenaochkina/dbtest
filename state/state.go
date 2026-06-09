package state

import (
	"context"
	"fmt"

	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a connection pool to the state store and runs schema migrations.
// Call pool.Close() when the test finishes.
func Connect(dsn string, tel *telemetry.Telemetry) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("state connect: %w", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("state ping: %w", err)
	}
	if err := migrate(context.Background(), pool); err != nil {
		pool.Close()
		return nil, err
	}
	tel.Logger.Info("state store connected")
	return pool, nil
}

// migrate is safe to call on every startup — CREATE TABLE IF NOT EXISTS is idempotent.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS runs (
			id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
			scenario   TEXT         NOT NULL,
			seed       BIGINT       NOT NULL,
			provider   TEXT         NOT NULL,
			started_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
			ended_at   TIMESTAMPTZ,
			passed     BOOLEAN
		)
	`)
	if err != nil {
		return fmt.Errorf("migrate runs table: %w", err)
	}

	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS checkpoints (
			id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
			run_id      UUID        NOT NULL,
			name        TEXT        NOT NULL,
			row_count   BIGINT      NOT NULL,
			stock_sum   BIGINT      NOT NULL,
			captured_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		return fmt.Errorf("migrate checkpoints table: %w", err)
	}

	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS benchmark_results (
			id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
			run_id            UUID        NOT NULL,
			provider          TEXT        NOT NULL,
			tps               FLOAT8      NOT NULL,
			latency_avg_ms    FLOAT8      NOT NULL,
			latency_stddev_ms FLOAT8      NOT NULL,
			scale_factor      INT         NOT NULL,
			clients           INT         NOT NULL,
			duration_seconds  FLOAT8      NOT NULL,
			created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		return fmt.Errorf("migrate benchmark_results table: %w", err)
	}

	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS clusters (
			id               TEXT        PRIMARY KEY,
			provider         TEXT        NOT NULL,
			dsn              TEXT        NOT NULL,
			status           TEXT        NOT NULL,
			provisioned_at   TIMESTAMPTZ NOT NULL,
			deprovisioned_at TIMESTAMPTZ
		)
	`)
	if err != nil {
		return fmt.Errorf("migrate clusters table: %w", err)
	}

	return nil
}
