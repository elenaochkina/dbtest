package state

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TPCCRow is one measured TPROC-C run.
type TPCCRow struct {
	Provider         string
	HighAvailability bool
	Repetition       int
	Warehouses       int
	VirtualUsers     int
	RampUpSeconds    int
	DurationSeconds  int
	BuildSeconds     float64
	NOPM             float64
	TPM              float64
	NewOrderP50Ms    float64
	NewOrderP95Ms    float64
	NewOrderP99Ms    float64
	Select1P50Ms     *float64 // nil for runs from a bench image without the baseline
	Select1P95Ms     *float64
	Metrics          map[string]float64
}

// TPCCSearchRow is the result of one tester client search.
type TPCCSearchRow struct {
	Provider          string
	HighAvailability  bool
	Warehouses        int
	RampUpSeconds     int
	DurationSeconds   int
	BuildSeconds      float64
	MinClients        int
	MaxClients        int
	Threshold         float64
	PeakClients       int
	PeakNOPM          float64
	SaturationClients int
	DeclineClients    *int // nil when throughput never fell below the threshold
	Trials            int
	Drift             float64
}

// execer is a pool or a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// SaveTPCCResult writes one run. A repeated insert for the same repetition is
// a no-op.
func SaveTPCCResult(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, row TPCCRow) error {
	if err := insertTPCCResult(ctx, pool, runID, row); err != nil {
		return fmt.Errorf("SaveTPCCResult: %w", err)
	}
	return nil
}

// SaveTPCCSearch writes a search's runs and its result in one transaction. A
// repeated save is a no-op.
func SaveTPCCSearch(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, search TPCCSearchRow, trials []TPCCRow) error {
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for _, row := range trials {
			if err := insertTPCCResult(ctx, tx, runID, row); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO tpcc_searches
				(run_id, provider, high_availability, warehouses, rampup_seconds, duration_seconds,
				 build_seconds, min_clients, max_clients, threshold, peak_clients, peak_nopm,
				 saturation_clients, decline_clients, trials, drift)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
			 ON CONFLICT (run_id) DO NOTHING`,
			runID, search.Provider, search.HighAvailability, search.Warehouses, search.RampUpSeconds,
			search.DurationSeconds, search.BuildSeconds, search.MinClients, search.MaxClients,
			search.Threshold, search.PeakClients, search.PeakNOPM, search.SaturationClients,
			search.DeclineClients, search.Trials, search.Drift,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("SaveTPCCSearch: %w", err)
	}
	return nil
}

func insertTPCCResult(ctx context.Context, db execer, runID uuid.UUID, row TPCCRow) error {
	metrics, err := json.Marshal(row.Metrics)
	if err != nil {
		return fmt.Errorf("encode metrics: %w", err)
	}
	_, err = db.Exec(ctx,
		`INSERT INTO tpcc_results
			(run_id, provider, high_availability, repetition, warehouses, virtual_users,
			 rampup_seconds, duration_seconds, build_seconds, nopm, tpm,
			 neword_p50_ms, neword_p95_ms, neword_p99_ms, select1_p50_ms, select1_p95_ms, metrics)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		 ON CONFLICT (run_id, repetition) DO NOTHING`,
		runID, row.Provider, row.HighAvailability, row.Repetition, row.Warehouses, row.VirtualUsers,
		row.RampUpSeconds, row.DurationSeconds, row.BuildSeconds, row.NOPM, row.TPM,
		row.NewOrderP50Ms, row.NewOrderP95Ms, row.NewOrderP99Ms, row.Select1P50Ms, row.Select1P95Ms, metrics,
	)
	return err
}
