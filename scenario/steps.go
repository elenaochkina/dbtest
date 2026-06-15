package scenario

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/elenaochkina/dbtest/pgadapter"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/validator"
	"github.com/elenaochkina/dbtest/workload"
)

// provisionStep adapts provider.Provision + WaitForReady into a step. It
// registers cluster teardown on the cleanup stack the instant provisioning
// succeeds — before waiting for readiness — so any later failure, including a
// failed WaitForReady, can never leak a paying cluster.
type provisionStep struct{}

func (provisionStep) Name() string { return "provision" }

func (provisionStep) Run(ctx context.Context, rc *RunContext) error {
	cluster, err := rc.Provider.Provision(ctx)
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

// restartStep adapts the provider's optional Restarter capability into a step.
// It forces an ungraceful restart of the running cluster, updates rc.Cluster
// with the refreshed DSN , and waits for the database to accept connections again before later steps run. 
// Fails clearly if the provider cannot restart.
type restartStep struct{}

func (restartStep) Name() string { return "restart" }

func (restartStep) Run(ctx context.Context, rc *RunContext) error {
	r, ok := rc.Provider.(provider.Restarter)
	if !ok {
		return fmt.Errorf("provider %T does not support restart", rc.Provider)
	}
	updated, err := r.Restart(ctx, rc.Cluster)
	if err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	rc.Cluster = updated
	if err := rc.Provider.WaitForReady(ctx, updated); err != nil {
		return fmt.Errorf("wait for ready after restart: %w", err)
	}
	return nil
}

// snapshotStep captures a content-hash fingerprint of each table under a label
// (e.g. "before_restart") and persists it to the run, establishing the baseline
// a later verifyStep compares against. It requires a state DB: the baseline must
// be durable, so with no StateRun there is nothing to record and the step fails
// rather than silently skipping a durability check.
type snapshotStep struct {
	label  string
	tables []string
}

func (snapshotStep) Name() string { return "snapshot" }

func (s snapshotStep) Run(ctx context.Context, rc *RunContext) error {
	if rc.StateRun == nil {
		return fmt.Errorf("snapshot requires a state DB (set STATE_DSN)")
	}
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
// the result under its own label (e.g. "after_restart"), and asserts it matches
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
	if rc.StateRun == nil {
		return fmt.Errorf("verify requires a state DB (set STATE_DSN)")
	}
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
			return fmt.Errorf("data changed across restart: table %q fingerprint %s != baseline %q %s",
				table, digest, s.baseline, baseline)
		}
	}
	return nil
}
