package scenario

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/elenaochkina/dbtest/pgadapter"
	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/state"
	"github.com/elenaochkina/dbtest/validator"
	"github.com/elenaochkina/dbtest/workload"
)

// provisionStep adapts provider.Provision + WaitForReady into a step. It
// registers cluster teardown on the cleanup stack the instant provisioning
// succeeds — before waiting for readiness — so any later failure, including a
// failed WaitForReady, can never leak a paying cluster.
type provisionStep struct{ request provider.ProvisionRequest }

func (provisionStep) Name() string { return "provision" }

func (s provisionStep) Run(ctx context.Context, rc *RunContext) error {
	cluster, err := rc.Provider.Provision(ctx, s.request)
	if err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	rc.Cluster = cluster

	rc.onCleanup(func(ctx context.Context) {
		if err := rc.Provider.Deprovision(ctx, cluster.ID); err != nil && rc.Tel != nil {
			rc.Tel.Logger.Error("deprovision failed",
				slog.String("cluster_id", cluster.ID),
				slog.Any("error", err),
			)
		}
	})

	if err := rc.Provider.WaitForReady(ctx, cluster); err != nil {
		return fmt.Errorf("wait for ready: %w", err)
	}
	return nil
}

// workloadStep adapts a single workload run into a step. Both the warehouse and
// pgbench data-plane actions are workloads, so one parameterised step covers
// them — the scenario picks which by name. It reads the DSN that provision put
// on the RunContext.
type workloadStep struct {
	name workload.WorkloadName
}

func (s workloadStep) Name() string { return string(s.name) }

func (s workloadStep) Run(ctx context.Context, rc *RunContext) error {
	w, err := workload.New(s.name, toWorkloadConfig(rc.Cfg))
	if err != nil {
		return fmt.Errorf("workload: %w", err)
	}
	result, err := w.Run(ctx, rc.Cluster.DSN, rc.Tel)
	if err != nil {
		return err
	}
	rc.Result = result
	return nil
}

// saveResultStep persists the last workload's result to its typed table in the
// state DB — currently benchmark (pgbench) results to benchmark_results. It is
// the persistence counterpart to workloadStep's compute: the workload returns a
// Result; the scenario layer, which owns the run and the state pool, stores it
// (the same split as Fingerprint vs snapshot/verify). Skipped only when the
// result has no typed home yet.
type saveResultStep struct{}

func (saveResultStep) Name() string { return "save-result" }

func (saveResultStep) Run(ctx context.Context, rc *RunContext) error {
	switch r := rc.Result.(type) {
	case nil:
		return nil
	case pgbench.Result:
		return state.SaveBenchmarkResult(ctx, rc.StatePool, rc.StateRun.ID, r, rc.Tel)
	default:
		if rc.Tel != nil {
			rc.Tel.Logger.Info("save-result: no typed persistence for result",
				slog.String("type", fmt.Sprintf("%T", r)))
		}
		return nil
	}
}

// killProcessStep adapts the provider's optional FailureInjector capability into
// a step. It injects an ungraceful failure (a forced process kill) into the
// running cluster, updates rc.Cluster with the refreshed DSN, and waits for the
// database to accept connections again before later steps run.
// Fails clearly if the provider cannot inject failures.
type killProcessStep struct{}

func (killProcessStep) Name() string { return "kill-process" }

func (killProcessStep) Run(ctx context.Context, rc *RunContext) error {
	r, ok := rc.Provider.(provider.FailureInjector)
	if !ok {
		return fmt.Errorf("provider %T does not support failure injection", rc.Provider)
	}
	updated, err := r.KillProcess(ctx, rc.Cluster)
	if err != nil {
		return fmt.Errorf("kill process: %w", err)
	}
	rc.Cluster = updated
	if err := rc.Provider.WaitForReady(ctx, updated); err != nil {
		return fmt.Errorf("wait for ready after kill: %w", err)
	}
	return nil
}

// snapshotStep captures a content-hash fingerprint of each table under a label
// (e.g. "before_kill_process") and persists it to the run, establishing the
// baseline a later verifyStep compares against. 
type snapshotStep struct {
	label  string
	tables []string
}

func (snapshotStep) Name() string { return "snapshot" }

func (s snapshotStep) Run(ctx context.Context, rc *RunContext) error {
	pool, err := pgadapter.Connect(rc.Cluster.DSN, rc.Tel)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	for _, table := range s.tables {
		digest, err := validator.Fingerprint(ctx, pool, table, rc.Tel)
		if err != nil {
			return err
		}
		if err := rc.StateRun.SaveFingerprint(ctx, s.label, table, digest); err != nil {
			return err
		}
	}
	return nil
}

// verifyStep re-fingerprints each table after a control-plane operation, persists
// the result under its own label (e.g. "after_kill_process"), and asserts it matches
// the baseline snapshot took. A mismatch means data did not survive unchanged —
// the failure the durability scenario exists to catch. The baseline is read back
// from the state DB, not from memory.
type verifyStep struct {
	label    string // where this step's fingerprints are saved
	baseline string // the snapshot label to compare against
	tables   []string
}

func (verifyStep) Name() string { return "verify" }

func (s verifyStep) Run(ctx context.Context, rc *RunContext) error {
	pool, err := pgadapter.Connect(rc.Cluster.DSN, rc.Tel)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	for _, table := range s.tables {
		digest, err := validator.Fingerprint(ctx, pool, table, rc.Tel)
		if err != nil {
			return err
		}
		if err := rc.StateRun.SaveFingerprint(ctx, s.label, table, digest); err != nil {
			return err
		}
		baseline, err := rc.StateRun.GetFingerprint(ctx, s.baseline, table)
		if err != nil {
			return fmt.Errorf("load baseline: %w", err)
		}
		if digest != baseline {
			return fmt.Errorf("data changed across crash recovery: table %q fingerprint %s != baseline %q %s",
				table, digest, s.baseline, baseline)
		}
	}
	return nil
}
