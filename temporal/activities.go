package temporal

import (
	"context"
	"fmt"

	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/validator"
	"github.com/elenaochkina/dbtest/workload"
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

// RunWarehouse runs the warehouse correctness workload and returns its post-run
// checksum.
func (a *Activities) RunWarehouse(ctx context.Context, input WorkloadInput) (validator.Checksum, error) {
	res, err := a.runWorkload(ctx, workload.Warehouse, input)
	if err != nil {
		return validator.Checksum{}, err
	}
	checksum, ok := res.(validator.Checksum)
	if !ok {
		return validator.Checksum{}, fmt.Errorf("warehouse: unexpected result type %T", res)
	}
	return checksum, nil
}

// RunPgbench runs the pgbench performance workload and returns its
// throughput/latency result.
func (a *Activities) RunPgbench(ctx context.Context, input WorkloadInput) (pgbench.Result, error) {
	res, err := a.runWorkload(ctx, workload.Pgbench, input)
	if err != nil {
		return pgbench.Result{}, err
	}
	result, ok := res.(pgbench.Result)
	if !ok {
		return pgbench.Result{}, fmt.Errorf("pgbench: unexpected result type %T", res)
	}
	return result, nil
}

// runWorkload resolves a workload by name and runs it against the cluster,
// building the DSN at call time so the password is never persisted separately.
// The concrete Result the workload returns is asserted by each caller.
func (a *Activities) runWorkload(ctx context.Context, name workload.WorkloadName, input WorkloadInput) (workload.Result, error) {
	w, err := workload.New(name, input.Config)
	if err != nil {
		return nil, fmt.Errorf("workload %q: %w", name, err)
	}
	return w.Run(ctx, input.Cluster.Target.URL(input.Cluster.Password), a.tel)
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
