package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/validator"
	"github.com/jackc/pgx/v5"
)

// RunConfig holds the input parameters for starting a new test run.
// Scenario must be consistent across runs — LastRun uses it to look up the
// previous run. Seed is the value passed to seedgen.New so the run can be
// reproduced exactly. Provider is "manual" for local runs (reserved for future
// values like "ci").
type RunConfig struct {
	Seed     int64
	Scenario string
	Provider string
}

// Run represents one row in the runs table of the state store.
// Conn is the connection to the state store — Run methods use it to execute
// queries. Never use Conn to query the database under test.
// ID is the primary key assigned by the database on insert; it links this run
// to its checkpoint rows.
// Logger is a structured logger scoped to this package, created from tel.Logger
// in StartRun. All Run methods write log lines through it.
type Run struct {
	Conn     *pgx.Conn
	ID       int64
	Scenario string
	Seed     int64
	Provider string
	Logger   *slog.Logger
}

// Connect opens a single connection to the state store at dsn and creates
// the runs and checkpoints tables if they do not already exist.
// tel is used for the "state store connected" log line.
// Returns an error if the database is unreachable or migration fails.
// Call conn.Close(ctx) when the test finishes to release the connection.
func Connect(dsn string, tel *telemetry.Telemetry) (*pgx.Conn, error) {
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("state connect: %w", err)
	}
	if err := migrate(context.Background(), conn); err != nil {
		conn.Close(context.Background())
		return nil, err
	}
	tel.Logger.Info("state store connected")
	return conn, nil
}

// migrate creates the runs and checkpoints tables if they do not already exist.
// Safe to call on every startup — CREATE TABLE IF NOT EXISTS is a no-op when
// the tables are already there.
func migrate(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS runs (
			id         BIGSERIAL    PRIMARY KEY,
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

	_, err = conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS checkpoints (
			id          BIGSERIAL   PRIMARY KEY,
			run_id      BIGINT      NOT NULL,
			name        TEXT        NOT NULL,
			row_count   BIGINT      NOT NULL,
			stock_sum   BIGINT      NOT NULL,
			captured_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		return fmt.Errorf("migrate checkpoints table: %w", err)
	}

	return nil
}

// StartRun inserts a new row into the runs table and returns a Run with the
// assigned database ID and all config fields populated. ended_at and passed
// are left NULL until End is called.
// tel is used to create a logger scoped to the state package; all subsequent
// Run method calls write through that logger without needing tel again.
// Call defer run.End(ctx, !t.Failed()) immediately after this function so the
// run is always closed even if the test panics or returns early.
func StartRun(ctx context.Context, conn *pgx.Conn, cfg RunConfig, tel *telemetry.Telemetry) (*Run, error) {
	var id int64
	err := conn.QueryRow(ctx,
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
		Conn:     conn,
		ID:       id,
		Scenario: cfg.Scenario,
		Seed:     cfg.Seed,
		Provider: cfg.Provider,
		Logger:   logger,
	}, nil
}

// LastRun returns the most recently completed, passing run for the given
// scenario. Returns nil, nil — not an error — when no previous run exists.
// This is the normal case on the first ever run; always check for nil before
// using the result. Returns an error only if the database query fails.
func LastRun(ctx context.Context, conn *pgx.Conn, scenario string) (*Run, error) {
	var r Run
	r.Conn = conn
	err := conn.QueryRow(ctx,
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

// Checkpoint inserts a named snapshot row into the checkpoints table for this
// run. name is the label you choose, e.g. "after_seed" or "after_orders".
// cs is the checksum captured at this point in the test.
func (r *Run) Checkpoint(ctx context.Context, name string, cs validator.Checksum) error {
	_, err := r.Conn.Exec(ctx,
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

// End sets ended_at and passed on the run's database row.
// Call with passed = !t.Failed() so the stored result reflects whether any
// test assertion fired during this run.
func (r *Run) End(ctx context.Context, passed bool) error {
	_, err := r.Conn.Exec(ctx,
		`UPDATE runs SET ended_at = now(), passed = $1 WHERE id = $2`,
		passed, r.ID,
	)
	if err != nil {
		return fmt.Errorf("End run %d: %w", r.ID, err)
	}
	r.Logger.Info("run ended", "run_id", r.ID, "passed", passed)
	return nil
}

// GetCheckpoint loads a named checkpoint row for this run from the state store.
// Returns an error if the checkpoint name does not exist for this run.
// Used internally by AssertMatchesPrior — the test itself never calls this directly.
func (r *Run) GetCheckpoint(ctx context.Context, name string) (validator.Checksum, error) {
	var cs validator.Checksum
	err := r.Conn.QueryRow(ctx,
		`SELECT row_count, stock_sum
		 FROM checkpoints
		 WHERE run_id = $1 AND name = $2`,
		r.ID, name,
	).Scan(&cs.RowCount, &cs.StockSum)
	if errors.Is(err, pgx.ErrNoRows) {
		return validator.Checksum{}, fmt.Errorf("checkpoint %q not found for run %d", name, r.ID)
	}
	if err != nil {
		return validator.Checksum{}, fmt.Errorf("GetCheckpoint %q: %w", name, err)
	}
	return cs, nil
}

// AssertMatchesPrior loads checkpointName from both current and prior runs and
// compares the checksums field by field. If they differ, t.Errorf is called
// with a message showing the checkpoint name, both run IDs, and the differing
// values. If they match, a confirmation line is logged.
// current is the run that just finished; prior is the run returned by LastRun.
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
		t.Errorf("checkpoint %q differs from prior run: current run_id=%d (row_count=%d stock_sum=%d) prior run_id=%d (row_count=%d stock_sum=%d)",
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
