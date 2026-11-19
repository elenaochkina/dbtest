package state

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SearchRow is the result of one tester client search.
type SearchRow struct {
	Tool              string
	Provider          string
	HighAvailability  bool
	Warehouses        *int // hammerdb only
	Scale             *int // pgbench only
	RampUpSeconds     int
	DurationSeconds   int
	BuildSeconds      float64
	MinClients        int
	MaxClients        int
	Threshold         float64
	PeakClients       int
	PeakThroughput    float64 // NOPM for hammerdb, TPS for pgbench
	SaturationClients int
	DeclineClients    *int // nil when throughput never fell below the threshold
	Trials            int  // not counting the drift run
	Drift             float64
}

// TrialRow is one timed run of a search.
type TrialRow struct {
	Clients    int
	Repeat     bool // the final drift run
	Throughput float64
	Metrics    map[string]float64
}

// SaveSearch writes a search's trials, numbered in run order, and its result
// in one transaction. A repeated save is a no-op.
func SaveSearch(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, search SearchRow, trials []TrialRow) error {
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for i, trial := range trials {
			metrics, err := json.Marshal(trial.Metrics)
			if err != nil {
				return fmt.Errorf("encode metrics: %w", err)
			}
			_, err = tx.Exec(ctx,
				`INSERT INTO search_trials (run_id, trial, clients, repeat, throughput, metrics)
				 VALUES ($1, $2, $3, $4, $5, $6)
				 ON CONFLICT (run_id, trial) DO NOTHING`,
				runID, i+1, trial.Clients, trial.Repeat, trial.Throughput, metrics)
			if err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO searches
				(run_id, tool, provider, high_availability, warehouses, scale, rampup_seconds,
				 duration_seconds, build_seconds, min_clients, max_clients, threshold, peak_clients,
				 peak_throughput, saturation_clients, decline_clients, trials, drift)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
			 ON CONFLICT (run_id) DO NOTHING`,
			runID, search.Tool, search.Provider, search.HighAvailability, search.Warehouses, search.Scale,
			search.RampUpSeconds, search.DurationSeconds, search.BuildSeconds, search.MinClients,
			search.MaxClients, search.Threshold, search.PeakClients, search.PeakThroughput,
			search.SaturationClients, search.DeclineClients, search.Trials, search.Drift,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("SaveSearch: %w", err)
	}
	return nil
}
