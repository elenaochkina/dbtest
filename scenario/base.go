package scenario

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/state"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/workload"
	"github.com/jackc/pgx/v5/pgxpool"
)

type baseScenario struct {
	cfg Config
	w   workload.Workload
}

func (s *baseScenario) Run(ctx context.Context, tel *telemetry.Telemetry) error {
	p, err := provider.Run(s.cfg.Provider, tel)
	if err != nil {
		return fmt.Errorf("provider: %w", err)
	}

	cluster, err := s.acquireCluster(ctx, p, tel)
	if err != nil {
		return err
	}
	defer s.releaseCluster(p, cluster, tel)

	if s.cfg.StatePool != nil {
		hbCtx, stopHB := context.WithCancel(context.Background())
		defer stopHB()
		go runHeartbeat(hbCtx, s.cfg.StatePool, cluster.ID)
	}

	if err := p.WaitForReady(ctx, cluster); err != nil {
		return fmt.Errorf("wait for ready: %w", err)
	}

	return s.w.Run(ctx, cluster.DSN, tel)
}

// acquireCluster reconnects to a recently-heartbeated cluster if one exists,
// deprovisions stale ones, and provisions fresh otherwise.
func (s *baseScenario) acquireCluster(ctx context.Context, p provider.Provider, tel *telemetry.Telemetry) (provider.ClusterInfo, error) {
	workloadName := string(s.cfg.Workload)

	if s.cfg.StatePool != nil {
		running, err := state.FindRunningClusters(ctx, s.cfg.StatePool, workloadName, string(s.cfg.Provider), tel)
		if err == nil && len(running) > 0 {
			for _, c := range running {
				if c.HeartbeatAt != nil && time.Since(*c.HeartbeatAt) < 2*time.Minute {
					if tel != nil {
						tel.Logger.Info("reconnecting to existing cluster",
							slog.String("cluster_id", c.ID),
							slog.String("workload", workloadName),
						)
					}
					return provider.ClusterInfo{ID: c.ID, DSN: c.DSN}, nil
				}
			}
			// All clusters have stale heartbeats — deprovision before provisioning fresh.
			for _, c := range running {
				if depErr := p.Deprovision(ctx, c.ID); depErr == nil {
					state.MarkDeprovisioned(ctx, s.cfg.StatePool, c.ID, tel)
				}
			}
		}
	}

	cluster, err := p.Provision(ctx)
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("provision: %w", err)
	}
	if s.cfg.StatePool != nil {
		if err := state.RecordCluster(ctx, s.cfg.StatePool, cluster, string(s.cfg.Provider), workloadName, tel); err != nil && tel != nil {
			tel.Logger.Warn("record cluster failed", slog.Any("error", err))
		}
	}
	return cluster, nil
}

func (s *baseScenario) releaseCluster(p provider.Provider, cluster provider.ClusterInfo, tel *telemetry.Telemetry) {
	depCtx := context.Background()
	if err := p.Deprovision(depCtx, cluster.ID); err != nil && tel != nil {
		tel.Logger.Error("deprovision failed",
			slog.String("cluster_id", cluster.ID),
			slog.Any("error", err),
		)
	}
	if s.cfg.StatePool != nil {
		if err := state.MarkDeprovisioned(depCtx, s.cfg.StatePool, cluster.ID, tel); err != nil && tel != nil {
			tel.Logger.Error("mark deprovisioned failed", slog.Any("error", err))
		}
	}
}

func runHeartbeat(ctx context.Context, pool *pgxpool.Pool, clusterID string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			state.UpdateHeartbeat(context.Background(), pool, clusterID)
		case <-ctx.Done():
			return
		}
	}
}
