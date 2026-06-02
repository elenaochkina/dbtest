package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/validator"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Scenario must be consistent across runs — LastRun uses it to look up the previous run.
type RunConfig struct {
	Seed     int64
	Scenario string
	Provider string
}

// Never use Pool to query the database under test.
type Run struct {
	Pool     *pgxpool.Pool
	ID       uuid.UUID
	Scenario string
	Seed     int64
	Provider string
	Logger   *slog.Logger
}

// ended_at and passed are left NULL until End is called.
// Call defer run.End(ctx, !t.Failed()) immediately after so the run is always closed.
func StartRun(ctx context.Context, pool *pgxpool.Pool, cfg RunConfig, tel *telemetry.Telemetry) (*Run, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx,
		`INSERT INTO runs (scenario, seed, provider, started_at)
		 VALUES ($1, $2, $3, now())
		 RETURNING id`,
		cfg.Scenario, cfg.Seed, cfg.Provider,
	).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("StartRun: %w", err)
	}

	logger := tel.Logger.With("package", "state")
	logger.Info("run started", "run_id", id, "scenario", cfg.Scenario, "seed", cfg.Seed)

	return &Run{
		Pool:     pool,
		ID:       id,
		Scenario: cfg.Scenario,
		Seed:     cfg.Seed,
		Provider: cfg.Provider,
		Logger:   logger,
	}, nil
}

// LastRun returns nil, nil — not an error — when no previous run exists.
func LastRun(ctx context.Context, pool *pgxpool.Pool, scenario string) (*Run, error) {
	var r Run
	r.Pool = pool
	err := pool.QueryRow(ctx,
		`SELECT id, scenario, seed, provider
		 FROM runs
		 WHERE scenario = $1
		   AND ended_at IS NOT NULL
		   AND passed = true
		 ORDER BY ended_at DESC
		 LIMIT 1`,
		scenario,
	).Scan(&r.ID, &r.Scenario, &r.Seed, &r.Provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("LastRun: %w", err)
	}
	return &r, nil
}

func (r *Run) Checkpoint(ctx context.Context, name string, cs validator.Checksum) error {
	_, err := r.Pool.Exec(ctx,
		`INSERT INTO checkpoints (run_id, name, row_count, stock_sum, captured_at)
		 VALUES ($1, $2, $3, $4, now())`,
		r.ID, name, cs.RowCount, cs.StockSum,
	)
	if err != nil {
		return fmt.Errorf("Checkpoint %q: %w", name, err)
	}
	r.Logger.Info("checkpoint saved", "run_id", r.ID, "name", name, "row_count", cs.RowCount, "stock_sum", cs.StockSum)
	return nil
}

// Call with passed = !t.Failed() so the result reflects whether any assertion fired.
func (r *Run) End(ctx context.Context, passed bool) error {
	_, err := r.Pool.Exec(ctx,
		`UPDATE runs SET ended_at = now(), passed = $1 WHERE id = $2`,
		passed, r.ID,
	)
	if err != nil {
		return fmt.Errorf("End run %s: %w", r.ID, err)
	}
	r.Logger.Info("run ended", "run_id", r.ID, "passed", passed)
	return nil
}

// GetCheckpoint is used internally by AssertMatchesPrior.
func (r *Run) GetCheckpoint(ctx context.Context, name string) (validator.Checksum, error) {
	var cs validator.Checksum
	err := r.Pool.QueryRow(ctx,
		`SELECT row_count, stock_sum
		 FROM checkpoints
		 WHERE run_id = $1 AND name = $2`,
		r.ID, name,
	).Scan(&cs.RowCount, &cs.StockSum)
	if errors.Is(err, pgx.ErrNoRows) {
		return validator.Checksum{}, fmt.Errorf("checkpoint %q not found for run %s", name, r.ID)
	}
	if err != nil {
		return validator.Checksum{}, fmt.Errorf("GetCheckpoint %q: %w", name, err)
	}
	return cs, nil
}

func AssertMatchesPrior(t *testing.T, ctx context.Context, current *Run, prior *Run, checkpointName string) {
	t.Helper()

	cur, err := current.GetCheckpoint(ctx, checkpointName)
	if err != nil {
		t.Errorf("AssertMatchesPrior: load current checkpoint: %v", err)
		return
	}
	priorCs, err := prior.GetCheckpoint(ctx, checkpointName)
	if err != nil {
		t.Errorf("AssertMatchesPrior: load prior checkpoint: %v", err)
		return
	}

	if cur.RowCount != priorCs.RowCount || cur.StockSum != priorCs.StockSum {
		t.Errorf("checkpoint %q differs from prior run: current run_id=%s (row_count=%d stock_sum=%d) prior run_id=%s (row_count=%d stock_sum=%d)",
			checkpointName,
			current.ID, cur.RowCount, cur.StockSum,
			prior.ID, priorCs.RowCount, priorCs.StockSum,
		)
		return
	}

	current.Logger.Info("checkpoint matches prior run",
		"checkpoint", checkpointName,
		"current_run_id", current.ID,
		"prior_run_id", prior.ID,
	)
}
