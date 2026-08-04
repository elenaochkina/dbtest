package activities

import (
	"context"
	"fmt"

	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/validator"
	"github.com/elenaochkina/dbtest/workload"
)

// WorkloadInput drives a workload activity: which cluster to run against and the
// workload parameters.
type WorkloadInput struct {
	Cluster provider.ClusterInfo
	Config  workload.Config
}

type WorkloadActivities struct {
	tel *telemetry.Telemetry
}

func NewWorkloadActivities(tel *telemetry.Telemetry) *WorkloadActivities {
	return &WorkloadActivities{tel: tel}
}

func (a *WorkloadActivities) RunWarehouse(ctx context.Context, input WorkloadInput) (validator.Checksum, error) {
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

func (a *WorkloadActivities) RunPgbench(ctx context.Context, input WorkloadInput) (pgbench.Result, error) {
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

// runWorkload resolves a workload by name, runs its setup phase if it has one,
// then runs it against the cluster — the single-shot path, for workflows that
// only want a throughput number. Workflows that must keep the phases apart,
// because setup is destructive, drive the bench container directly instead.
func (a *WorkloadActivities) runWorkload(ctx context.Context, name workload.WorkloadName, input WorkloadInput) (workload.Result, error) {
	w, err := workload.New(name, input.Config)
	if err != nil {
		return nil, fmt.Errorf("workload %q: %w", name, err)
	}
	dsn := input.Cluster.Target.URL(input.Cluster.Password)
	if init, ok := w.(workload.Initializer); ok {
		if err := init.Initialize(ctx, dsn, a.tel); err != nil {
			return nil, fmt.Errorf("workload %q initialize: %w", name, err)
		}
	}
	return w.Run(ctx, dsn, a.tel)
}
