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

// migrate creates the tables that do not exist yet. It does not change
// existing tables, so a schema change needs a new state db.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS runs (
			id                     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
			scenario               TEXT        NOT NULL,
			seed                   BIGINT      NOT NULL,
			provider               TEXT        NOT NULL,
			workflow_id            TEXT,
			disruption             TEXT,
			repetitions            INT,
			instance_class         TEXT,
			high_availability      BOOLEAN,
			vcpu                   FLOAT8,
			memory_mib             INT,
			disk_gib               INT,
			region                 TEXT,
			postgres_version       TEXT,
			started_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
			provision_started_at   TIMESTAMPTZ,
			provisioned_at         TIMESTAMPTZ,
			ready_at               TIMESTAMPTZ,
			deprovision_started_at TIMESTAMPTZ,
			deprovisioned_at       TIMESTAMPTZ,
			ended_at               TIMESTAMPTZ,
			passed                 BOOLEAN,
			provision_seconds      FLOAT8 GENERATED ALWAYS AS (EXTRACT(EPOCH FROM provisioned_at - provision_started_at)) STORED,
			ready_seconds          FLOAT8 GENERATED ALWAYS AS (EXTRACT(EPOCH FROM ready_at - provisioned_at)) STORED,
			deprovision_seconds    FLOAT8 GENERATED ALWAYS AS (EXTRACT(EPOCH FROM deprovisioned_at - deprovision_started_at)) STORED
		)`,
		`CREATE TABLE IF NOT EXISTS checkpoints (
			id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
			run_id      UUID        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
			name        TEXT        NOT NULL,
			row_count   BIGINT      NOT NULL,
			stock_sum   BIGINT      NOT NULL,
			captured_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS benchmark_results (
			id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
			run_id            UUID        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
			provider          TEXT        NOT NULL,
			tps               FLOAT8      NOT NULL,
			latency_avg_ms    FLOAT8      NOT NULL,
			latency_stddev_ms FLOAT8      NOT NULL,
			scale_factor      INT         NOT NULL,
			clients           INT         NOT NULL,
			duration_seconds  FLOAT8      NOT NULL,
			created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS fingerprints (
			id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
			run_id      UUID        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
			name        TEXT        NOT NULL,
			table_name  TEXT        NOT NULL,
			digest      TEXT        NOT NULL,
			captured_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// probe_interval_ms is the resolution of the two downtime figures, so rows
		// recorded at different intervals are not comparable and the query has to be
		// able to tell. UNIQUE (run_id, repetition) makes a retried insert a no-op.
		`CREATE TABLE IF NOT EXISTS downtime_results (
			id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
			run_id               UUID        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
			provider             TEXT        NOT NULL,
			disruption           TEXT        NOT NULL,
			repetition           INT         NOT NULL,
			disrupted_at         TIMESTAMPTZ NOT NULL,
			usable_again_at      TIMESTAMPTZ NOT NULL,
			readable_downtime_ms FLOAT8      NOT NULL,
			writable_downtime_ms FLOAT8      NOT NULL,
			lost_commits         BIGINT      NOT NULL,
			probe_interval_ms    FLOAT8      NOT NULL,
			probe_failures       INT         NOT NULL,
			probe_errors         JSONB       NOT NULL,
			created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE (run_id, repetition)
		)`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}
