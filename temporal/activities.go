package temporal

import (
	"context"
	"fmt"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Activities holds the live dependencies the worker injects once at startup: the
// state DB pool and telemetry.
type Activities struct {
	statePool *pgxpool.Pool
	tel       *telemetry.Telemetry
}

func NewActivities(statePool *pgxpool.Pool, tel *telemetry.Telemetry) *Activities {
	return &Activities{
		statePool: statePool,
		tel:       tel,
	}
}
func (a *Activities) Provision(ctx context.Context, input ProvisionInput) (provider.ClusterInfo, error) {

	p, err := provider.Run(input.Provider, a.tel)
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("provider %q: %w", input.Provider, err)
	}
	cluster, err := p.Provision(ctx, input.Request, input.Token, input.Password)
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("provision: %w", err)
	}
	return cluster, nil
}

// WaitForReady blocks until the cluster accepts connections. Split from Provision
// so the workflow can register teardown before waiting — a readiness failure or
// worker crash then still tears the cluster down instead of orphaning it.
func (a *Activities) WaitForReady(ctx context.Context, input WaitForReadyInput) error {
	p, err := provider.Run(input.Provider, a.tel)
	if err != nil {
		return fmt.Errorf("provider %q: %w", input.Provider, err)
	}
	return p.WaitForReady(ctx, input.Cluster)
}

// Deprovision tears the cluster down. It is idempotent: providers treat an
// already-deleted cluster as success (e.g. RDS NotFound), so Temporal may safely
// retry or replay it without a second, failing delete.
func (a *Activities) Deprovision(ctx context.Context, input DeprovisionInput) error {
	p, err := provider.Run(input.Provider, a.tel)
	if err != nil {
		return fmt.Errorf("provider %q: %w", input.Provider, err)
	}
	return p.Deprovision(ctx, input.ClusterID)
}
