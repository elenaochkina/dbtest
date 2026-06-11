package state

import (
	"context"
	"fmt"
	"time"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ClusterRecord is a row from the clusters table.
type ClusterRecord struct {
	ID          string
	Provider    string
	DSN         string
	Scenario    string
	Status      string
	HeartbeatAt *time.Time
}

// RecordCluster writes a new cluster row with status='running' immediately after
// provisioning. If the process crashes before Deprovision fires, the row survives
// and a future cleanup job can find and deprovision it.
func RecordCluster(ctx context.Context, pool *pgxpool.Pool, cluster provider.ClusterInfo, providerName, scenarioName string, tel *telemetry.Telemetry) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO clusters (id, provider, dsn, scenario, status, provisioned_at)
		 VALUES ($1, $2, $3, $4, 'running', now())`,
		cluster.ID, providerName, cluster.DSN, scenarioName,
	)
	if err != nil {
		return fmt.Errorf("RecordCluster: %w", err)
	}
	if tel != nil {
		tel.Logger.With("package", "state").Info("cluster recorded",
			"cluster_id", cluster.ID,
			"provider", providerName,
			"scenario", scenarioName,
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

// FindRunningClusters returns all clusters with status='running' for the given
// scenario and provider, ordered newest first.
func FindRunningClusters(ctx context.Context, pool *pgxpool.Pool, scenarioName, providerName string, tel *telemetry.Telemetry) ([]ClusterRecord, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, provider, dsn, scenario, status, heartbeat_at
		 FROM clusters
		 WHERE status = 'running'
		   AND scenario = $1
		   AND provider = $2
		 ORDER BY provisioned_at DESC`,
		scenarioName, providerName,
	)
	if err != nil {
		return nil, fmt.Errorf("FindRunningClusters: %w", err)
	}
	defer rows.Close()

	var records []ClusterRecord
	for rows.Next() {
		var r ClusterRecord
		if err := rows.Scan(&r.ID, &r.Provider, &r.DSN, &r.Scenario, &r.Status, &r.HeartbeatAt); err != nil {
			return nil, fmt.Errorf("FindRunningClusters scan: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// UpdateHeartbeat stamps heartbeat_at to now, proving the process is still alive.
// Call this from a background goroutine every 30 seconds while the cluster is in use.
func UpdateHeartbeat(ctx context.Context, pool *pgxpool.Pool, clusterID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE clusters SET heartbeat_at = now() WHERE id = $1`,
		clusterID,
	)
	if err != nil {
		return fmt.Errorf("UpdateHeartbeat: %w", err)
	}
	return nil
}
