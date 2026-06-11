package scenario

import (
	"context"
	"fmt"
	"time"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/workload"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Scenario owns the full lifecycle: provision cluster, run workload, deprovision cluster.
type Scenario interface {
	Run(ctx context.Context, tel *telemetry.Telemetry) error
}

// Config combines control plane (provider) and data plane (workload) parameters.
type Config struct {
	Provider  provider.ProviderName
	Workload  workload.WorkloadName
	StatePool *pgxpool.Pool // optional — state tracking and reconnect skipped when nil
	// warehouse
	Seed       int64
	Warehouses int
	// pgbench
	ScaleFactor int
	Clients     int
	Duration    time.Duration
}

// New constructs a Scenario for the given config.
// The workload is resolved from the workload registry; an unknown workload name returns an error.
func New(cfg Config) (Scenario, error) {
	w, err := workload.New(cfg.Workload, toWorkloadConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("workload: %w", err)
	}
	return &baseScenario{cfg: cfg, w: w}, nil
}

func toWorkloadConfig(cfg Config) workload.Config {
	return workload.Config{
		Seed:         cfg.Seed,
		Warehouses:   cfg.Warehouses,
		ScaleFactor:  cfg.ScaleFactor,
		Clients:      cfg.Clients,
		Duration:     cfg.Duration,
		ProviderName: string(cfg.Provider),
	}
}
