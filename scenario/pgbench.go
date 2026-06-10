package scenario

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/telemetry"
)

func init() {
	Register(Pgbench, func(cfg Config) Scenario {
		return &pgbenchScenario{cfg: cfg}
	})
}

type pgbenchScenario struct{ cfg Config }

func (s *pgbenchScenario) Name() string { return string(Pgbench) }

func (s *pgbenchScenario) Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) error {
	result, err := pgbench.RunLocal(ctx, dsn, pgbench.Config{
		ScaleFactor: s.cfg.ScaleFactor,
		Clients:     s.cfg.Clients,
		Duration:    s.cfg.Duration,
		Provider:    s.cfg.ProviderName,
	}, tel)
	if err != nil {
		return fmt.Errorf("pgbench: %w", err)
	}
	if tel != nil {
		tel.Logger.Info("pgbench complete",
			slog.Float64("tps", result.TPS),
			slog.Float64("latency_avg_ms", result.LatencyAvgMs),
			slog.Float64("latency_stddev_ms", result.LatencyStddevMs),
		)
	}
	return nil
}
