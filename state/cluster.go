package state

import (
	"context"
	"fmt"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RecordCluster writes a new cluster row with status='running' immediately after
// provisioning. If the process crashes before Deprovision fires, the row survives
// and a future cleanup job can find and deprovision it.
func RecordCluster(ctx context.Context, pool *pgxpool.Pool, cluster provider.ClusterInfo, providerName string, tel *telemetry.Telemetry) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO clusters (id, provider, dsn, status, provisioned_at)
		 VALUES ($1, $2, $3, 'running', now())`,
		cluster.ID, providerName, cluster.DSN,
	)
	if err != nil {
		return fmt.Errorf("RecordCluster: %w", err)
	}
	if tel != nil {
		tel.Logger.With("package", "state").Info("cluster recorded",
			"cluster_id", cluster.ID,
			"provider", providerName,
		)
	}
	return nil
}

// MarkDeprovisioned sets status='deprovisioned' and records the timestamp.
// Call this after Deprovision succeeds.
func MarkDeprovisioned(ctx context.Context, pool *pgxpool.Pool, clusterID string, tel *telemetry.Telemetry) error {
	_, err := pool.Exec(ctx,
		`UPDATE clusters SET status = 'deprovisioned', deprovisioned_at = now() WHERE id = $1`,
		clusterID,
	)
	if err != nil {
		return fmt.Errorf("MarkDeprovisioned: %w", err)
	}
	if tel != nil {
		tel.Logger.With("package", "state").Info("cluster deprovisioned", "cluster_id", clusterID)
	}
	return nil
}
