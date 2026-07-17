package temporal

import (
	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"
)

// PgBenchWorkflow runs one benchmark end to end: open a run record,
// provision a cluster, run the workloads, persist the result, then always tear
// the cluster down and close the run.
// EndRun is registered first so it runs after Deprovision, so passed reflects the whole workflow, teardown included.
func PgBenchWorkflow(ctx workflow.Context, cfg PgBenchWorkflowConfig) (err error) {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	var a *Activities

	var runID uuid.UUID
	if err = workflow.ExecuteActivity(ctx, a.StartRun, StartRunInput{
		Scenario: "pg-bench",
		Seed:     cfg.Workload.Seed,
		Provider: cfg.Provider,
	}).Get(ctx, &runID); err != nil {
		return err
	}

	defer func() {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		dctx = workflow.WithActivityOptions(dctx, defaultActivityOptions)
		_ = workflow.ExecuteActivity(dctx, a.EndRun, EndRunInput{
			RunID:  runID,
			Passed: err == nil,
		}).Get(dctx, nil)
	}()

	// runID serves as the provisioning idempotency token.
	var password string
	if err = workflow.SideEffect(ctx, func(workflow.Context) any {
		return uuid.NewString()
	}).Get(&password); err != nil {
		return err
	}

	var cluster provider.ClusterInfo
	if err = workflow.ExecuteActivity(ctx, a.Provision, ProvisionInput{
		Provider: cfg.Provider,
		Request:  cfg.Request,
		Token:    runID.String(),
		Password: password,
	}).Get(ctx, &cluster); err != nil {
		return err
	}
	// Registered after Provision → runs BEFORE EndRun.
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

	workloadCfg := cfg.Workload
	workloadCfg.ProviderName = string(cfg.Provider)
	wl := WorkloadInput{Cluster: cluster, Config: workloadCfg}

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
