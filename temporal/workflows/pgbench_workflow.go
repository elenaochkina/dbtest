package workflows

import (
	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/activities"
	"github.com/elenaochkina/dbtest/workload"
	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"
)

// PgBenchWorkflowConfig is the input to PgBenchWorkflow.
// It holds provisioning (Provider, Request) and the workload params.
type PgBenchWorkflowConfig struct {
	Provider provider.ProviderName
	Request  provider.ProvisionRequest // cluster resource spec the caller declares
	Workload workload.Config
}

// PgBenchWorkflow runs one benchmark end to end: open a run record,
// provision a cluster, run the workloads, persist the result, then always tear
// the cluster down and close the run.
// EndRun is registered first so it runs after Deprovision, so passed reflects the whole workflow, teardown included.
func PgBenchWorkflow(ctx workflow.Context, cfg PgBenchWorkflowConfig) (err error) {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	// Typed-nil receivers: Temporal resolves each ExecuteActivity to the method's
	// registered name, so no real instance is needed here.
	var (
		runs *activities.SaveResultActivities
		prov *activities.ProviderActivities
		work *activities.WorkloadActivities
	)

	var runID uuid.UUID
	if err = workflow.ExecuteActivity(ctx, runs.StartRun, activities.StartRunInput{
		Scenario: "pg-bench",
		Seed:     cfg.Workload.Seed,
		Provider: cfg.Provider,
	}).Get(ctx, &runID); err != nil {
		return err
	}

	defer func() {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		dctx = workflow.WithActivityOptions(dctx, defaultActivityOptions)
		_ = workflow.ExecuteActivity(dctx, runs.EndRun, activities.EndRunInput{
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
	if err = workflow.ExecuteActivity(ctx, prov.Provision, activities.ProvisionInput{
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
		if derr := workflow.ExecuteActivity(dctx, prov.Deprovision, activities.DeprovisionInput{
			Provider:  cfg.Provider,
			ClusterID: cluster.ID,
		}).Get(dctx, nil); derr != nil && err == nil {
			err = derr
		}
	}()

	if err = workflow.ExecuteActivity(ctx, prov.WaitForReady, activities.WaitForReadyInput{
		Provider: cfg.Provider,
		Cluster:  cluster,
	}).Get(ctx, nil); err != nil {
		return err
	}

	workloadCfg := cfg.Workload
	workloadCfg.ProviderName = string(cfg.Provider)
	wl := activities.WorkloadInput{Cluster: cluster, Config: workloadCfg}

	// Warehouse is the correctness workload;
	if err = workflow.ExecuteActivity(ctx, work.RunWarehouse, wl).Get(ctx, nil); err != nil {
		return err
	}
	var result pgbench.Result
	if err = workflow.ExecuteActivity(ctx, work.RunPgbench, wl).Get(ctx, &result); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(ctx, runs.SaveResult, activities.SaveResultInput{
		RunID:  runID,
		Result: result,
	}).Get(ctx, nil); err != nil {
		return err
	}

	return nil
}
