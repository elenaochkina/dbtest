package state

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DowntimeRow is one disruption as the probe observed it.
type DowntimeRow struct {
	Provider            string
	Disruption          string
	Repetition          int
	ReachableDowntimeMs float64
	WritableDowntimeMs  float64
	LostCommits         int64
	ProbeIntervalMs     float64
	ProbeFailures       int
	ProbeErrors         map[string]int
}

// SaveDowntimeResults writes one row per disruption. A repeated insert for the
// same repetition is a no-op, so a retried activity cannot double-count.
func SaveDowntimeResults(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, rows []DowntimeRow, tel *telemetry.Telemetry) error {
	for _, row := range rows {
		errors := row.ProbeErrors
		if errors == nil {
			errors = map[string]int{}
		}
		encoded, err := json.Marshal(errors)
		if err != nil {
			return fmt.Errorf("SaveDowntimeResults: encode probe errors: %w", err)
		}

		_, err = pool.Exec(ctx,
			`INSERT INTO downtime_results
				(run_id, provider, disruption, repetition, reachable_downtime_ms,
				 writable_downtime_ms, lost_commits, probe_interval_ms, probe_failures, probe_errors)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			 ON CONFLICT (run_id, repetition) DO NOTHING`,
			runID, row.Provider, row.Disruption, row.Repetition, row.ReachableDowntimeMs,
			row.WritableDowntimeMs, row.LostCommits, row.ProbeIntervalMs, row.ProbeFailures, encoded,
		)
		if err != nil {
			return fmt.Errorf("SaveDowntimeResults: %w", err)
		}
	}

	if tel != nil {
		tel.Logger.With("package", "state").Info("saved downtime results",
			"run_id", runID, "rows", len(rows))
	}
	return nil
}
