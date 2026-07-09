package temporal

import (
	"context"
	"fmt"

	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/state"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/validator"
	"github.com/elenaochkina/dbtest/workload"
	"github.com/google/uuid"
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

// WaitForReady blocks until the cluster accepts connections.
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
func (a *Activities) runWorkload(ctx context.Context, name workload.WorkloadName, input WorkloadInput) (workload.Result, error) {
	w, err := workload.New(name, input.Config)
	if err != nil {
		return nil, fmt.Errorf("workload %q: %w", name, err)
	}
	return w.Run(ctx, input.Cluster.Target.URL(input.Cluster.Password), a.tel)
}

// StartRun opens a runs row for this execution and returns its DB-generated id,
// which SaveResult and EndRun reference.
func (a *Activities) StartRun(ctx context.Context, input StartRunInput) (uuid.UUID, error) {
	run, err := state.StartRun(ctx, a.statePool, state.RunConfig{
		Seed:     input.Seed,
		Scenario: input.Scenario,
		Provider: string(input.Provider),
	}, a.tel)
	if err != nil {
		return uuid.UUID{}, err
	}
	return run.ID, nil
}

// SaveResult persists a pgbench result to the state DB under an existing run.
func (a *Activities) SaveResult(ctx context.Context, input SaveResultInput) error {
	return state.SaveBenchmarkResult(ctx, a.statePool, input.RunID, input.Result, a.tel)
}

// EndRun closes the runs row (ended_at, passed).
func (a *Activities) EndRun(ctx context.Context, input EndRunInput) error {
	run := &state.Run{Pool: a.statePool, ID: input.RunID, Logger: a.tel.Logger}
	return run.End(ctx, input.Passed)
}

// Deprovision tears the cluster down.

func (a *Activities) Deprovision(ctx context.Context, input DeprovisionInput) error {
	p, err := provider.Run(input.Provider, a.tel)
	if err != nil {
		return fmt.Errorf("provider %q: %w", input.Provider, err)
	}
	return p.Deprovision(ctx, input.ClusterID)
}
