package state

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TPCCRow is one measured TPROC-C run.
type TPCCRow struct {
	Provider        string
	Repetition      int
	Warehouses      int
	VirtualUsers    int
	RampUpSeconds   int
	DurationSeconds int
	BuildSeconds    float64
	NOPM            float64
	TPM             float64
	NewOrderP50Ms   float64
	NewOrderP95Ms   float64
	NewOrderP99Ms   float64
	Select1P50Ms    *float64 // nil for runs from a bench image without the baseline
	Select1P95Ms    *float64
	Metrics         map[string]float64
}

// SaveTPCCResult writes one run. A repeated insert for the same repetition is
// a no-op.
func SaveTPCCResult(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, row TPCCRow) error {
	metrics, err := json.Marshal(row.Metrics)
	if err != nil {
		return fmt.Errorf("SaveTPCCResult: encode metrics: %w", err)
	}
	_, err = pool.Exec(ctx,
		`INSERT INTO tpcc_results
			(run_id, provider, repetition, warehouses, virtual_users,
			 rampup_seconds, duration_seconds, build_seconds, nopm, tpm,
			 neword_p50_ms, neword_p95_ms, neword_p99_ms, select1_p50_ms, select1_p95_ms, metrics)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		 ON CONFLICT (run_id, repetition) DO NOTHING`,
		runID, row.Provider, row.Repetition, row.Warehouses, row.VirtualUsers,
		row.RampUpSeconds, row.DurationSeconds, row.BuildSeconds, row.NOPM, row.TPM,
		row.NewOrderP50Ms, row.NewOrderP95Ms, row.NewOrderP99Ms, row.Select1P50Ms, row.Select1P95Ms, metrics,
	)
	if err != nil {
		return fmt.Errorf("SaveTPCCResult: %w", err)
	}
	return nil
}
