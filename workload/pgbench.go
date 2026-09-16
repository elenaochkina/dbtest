package workload

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/telemetry"
)

func init() {
	Register(Pgbench, func(cfg Config) Workload {
		return &pgbenchWorkload{cfg: cfg}
	})
}

type pgbenchWorkload struct{ cfg Config }

func (s *pgbenchWorkload) Name() string { return string(Pgbench) }

// pgbenchConfig maps the shared workload Config onto pgbench's own.
func (s *pgbenchWorkload) pgbenchConfig() pgbench.Config {
	return pgbench.Config{
		ScaleFactor: s.cfg.ScaleFactor,
		Clients:     s.cfg.Clients,
		Duration:    s.cfg.Duration,
		Provider:    s.cfg.ProviderName,
	}
}

// Initialize creates and populates the pgbench tables. It satisfies Initializer,
// so callers can act between setup and the measured phase.
func (s *pgbenchWorkload) Initialize(ctx context.Context, dsn string, _ *telemetry.Telemetry) error {
	if err := pgbench.Initialize(ctx, dsn, s.pgbenchConfig()); err != nil {
		return fmt.Errorf("pgbench: %w", err)
	}
	return nil
}

// Run assumes Initialize has already run — the tables must exist.
func (s *pgbenchWorkload) Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) (Result, error) {
	result, err := pgbench.Run(ctx, dsn, s.pgbenchConfig(), tel)
	if err != nil {
		return nil, fmt.Errorf("pgbench: %w", err)
	}
	if tel != nil {
		tel.Logger.Info("pgbench complete",
			slog.Float64("tps", result.TPS),
			slog.Float64("latency_avg_ms", result.LatencyAvgMs),
			slog.Float64("latency_stddev_ms", result.LatencyStddevMs),
		)
	}
	return result, nil
}
