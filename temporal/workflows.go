package temporal

import (
	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/workload"
	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"
)

// PgBenchWorkflow runs one benchmark end to end: open a run record,
// provision a cluster, run the workloads, persist the result, then always tear
// the cluster down and close the run. It is the durable port of scenario.Run.
//
// The named return err is the seam the two deferred activities read. EndRun
// stamps the run passed=(err==nil); Deprovision folds its own failure into err.
// EndRun is registered first so it runs LAST (defers are LIFO) — after
// Deprovision — so passed reflects the whole workflow, teardown included.
func PgBenchWorkflow(ctx workflow.Context, cfg Config) (err error) {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	var a *Activities

	// Open the run record; it brackets the whole execution.
	var runID uuid.UUID
	if err = workflow.ExecuteActivity(ctx, a.StartRun, StartRunInput{
		Scenario: "pg-bench",
		Seed:     cfg.Seed,
		Provider: cfg.Provider,
	}).Get(ctx, &runID); err != nil {
		return err // no run row created → nothing to close
	}
	// Close the run on every exit path. Registered first → runs LAST, so its
	// passed flag sees the final err, including a deprovision failure below.
	defer func() {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		dctx = workflow.WithActivityOptions(dctx, defaultActivityOptions)
		_ = workflow.ExecuteActivity(dctx, a.EndRun, EndRunInput{
			RunID:  runID,
			Passed: err == nil,
		}).Get(dctx, nil)
	}()

	type provisionIdentity struct{ Token, Password string }
	var pid provisionIdentity
	if err = workflow.SideEffect(ctx, func(workflow.Context) any {
		return provisionIdentity{Token: uuid.NewString(), Password: uuid.NewString()}
	}).Get(&pid); err != nil {
		return err
	}

	var cluster provider.ClusterInfo
	if err = workflow.ExecuteActivity(ctx, a.Provision, ProvisionInput{
		Provider: cfg.Provider,
		Request:  cfg.Request,
		Token:    pid.Token,
		Password: pid.Password,
	}).Get(ctx, &cluster); err != nil {
		return err // nothing provisioned → nothing to tear down
	}
	// Durable teardown. Registered after Provision → runs BEFORE EndRun. Folds
	// its failure into err so a failed teardown marks the run failed.
	defer func() {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		dctx = workflow.WithActivityOptions(dctx, defaultActivityOptions)
		if derr := workflow.ExecuteActivity(dctx, a.Deprovision, DeprovisionInput{
			Provider:  cfg.Provider,
			ClusterID: cluster.ID,
		}).Get(dctx, nil); derr != nil && err == nil {
			err = derr
		}
	}()

	if err = workflow.ExecuteActivity(ctx, a.WaitForReady, WaitForReadyInput{
		Provider: cfg.Provider,
		Cluster:  cluster,
	}).Get(ctx, nil); err != nil {
		return err
	}

	// Workloads run against the ready cluster. Both take the same input; each
	// reads the config fields it needs.
	wl := WorkloadInput{
		Cluster: cluster,
		Config: workload.Config{
			Seed:         cfg.Seed,
			Warehouses:   cfg.Warehouses,
			ScaleFactor:  cfg.ScaleFactor,
			Clients:      cfg.Clients,
			Duration:     cfg.Duration,
			ProviderName: string(cfg.Provider),
		},
	}
	// Warehouse is the correctness workload;
	if err = workflow.ExecuteActivity(ctx, a.RunWarehouse, wl).Get(ctx, nil); err != nil {
		return err
	}
	var result pgbench.Result
	if err = workflow.ExecuteActivity(ctx, a.RunPgbench, wl).Get(ctx, &result); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(ctx, a.SaveResult, SaveResultInput{
		RunID:  runID,
		Result: result,
	}).Get(ctx, nil); err != nil {
		return err
	}

	return nil
}
