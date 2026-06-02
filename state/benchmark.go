package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func SaveBenchmarkResult(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, result pgbench.Result, tel *telemetry.Telemetry) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO benchmark_results
			(run_id, provider, tps, latency_avg_ms, latency_stddev_ms, scale_factor, clients, duration_seconds)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		runID, result.Provider, result.TPS, result.LatencyAvgMs, result.LatencyStddevMs,
		result.ScaleFactor, result.Clients, result.Duration.Seconds(),
	)
	if err != nil {
		return fmt.Errorf("SaveBenchmarkResult: %w", err)
	}
	if tel != nil {
		tel.Logger.With("package", "state").Info("saved benchmark result", "provider", result.Provider, "tps", result.TPS)
	}
	return nil
}

// GetLastBenchmarkResult returns nil, nil — not an error — when no previous result exists.
func GetLastBenchmarkResult(ctx context.Context, pool *pgxpool.Pool, provider string, tel *telemetry.Telemetry) (*pgbench.Result, error) {
	var r pgbench.Result
	var durationSeconds float64
	err := pool.QueryRow(ctx,
		`SELECT provider, tps, latency_avg_ms, latency_stddev_ms, scale_factor, clients, duration_seconds
		 FROM benchmark_results
		 WHERE provider = $1
		 ORDER BY created_at DESC
		 LIMIT 1`,
		provider,
	).Scan(&r.Provider, &r.TPS, &r.LatencyAvgMs, &r.LatencyStddevMs, &r.ScaleFactor, &r.Clients, &durationSeconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetLastBenchmarkResult: %w", err)
	}
	r.Duration = time.Duration(durationSeconds * float64(time.Second))
	if tel != nil {
		tel.Logger.With("package", "state").Info("loaded previous benchmark result", "provider", r.Provider, "tps", r.TPS)
	}
	return &r, nil
}
