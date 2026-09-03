package workflows

import (
	"fmt"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/activities"
	"github.com/elenaochkina/dbtest/workload"
	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"
)

type CrashRecoveryWorkflowConfig struct {
	Provider provider.ProviderName
	Request  provider.ProvisionRequest
	Workload workload.Config
}

// crashRecoveryTables hold the committed data the warehouse workload populates;
var crashRecoveryTables = []string{"warehouse", "orders"}

// CrashRecoveryWorkflow tests that committed data survives an ungraceful crash:
func CrashRecoveryWorkflow(ctx workflow.Context, cfg CrashRecoveryWorkflowConfig) (err error) {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	var (
		runs *activities.SaveResultActivities
		prov *activities.ProviderActivities
		work *activities.WorkloadActivities
		dur  *activities.DurabilityActivities
	)

	var runID uuid.UUID
	if err = workflow.ExecuteActivity(ctx, runs.StartRun, activities.StartRunInput{
		Scenario: "crash-recovery",
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

	// Seed committed data with the warehouse workload.
	workloadCfg := cfg.Workload
	workloadCfg.ProviderName = string(cfg.Provider)
	if err = workflow.ExecuteActivity(ctx, work.RunWarehouse, activities.WorkloadInput{
		Cluster: cluster,
		Config:  workloadCfg,
	}).Get(ctx, nil); err != nil {
		return err
	}

	// Fingerprint before the crash; keep it in a workflow variable.
	var before map[string]string
	if err = workflow.ExecuteActivity(ctx, dur.Fingerprint, activities.FingerprintInput{
		Cluster: cluster,
		Tables:  crashRecoveryTables,
	}).Get(ctx, &before); err != nil {
		return err
	}

	// Kill the instance, then wait for it to recover.
	if err = workflow.ExecuteActivity(ctx, prov.KillProcess, activities.KillProcessInput{
		Provider: cfg.Provider,
		Cluster:  cluster,
	}).Get(ctx, &cluster); err != nil {
		return err
	}
	if err = workflow.ExecuteActivity(ctx, prov.WaitForReady, activities.WaitForReadyInput{
		Provider: cfg.Provider,
		Cluster:  cluster,
	}).Get(ctx, nil); err != nil {
		return err
	}

	// Fingerprint after recovery and compare against the in-memory baseline.
	var after map[string]string
	if err = workflow.ExecuteActivity(ctx, dur.Fingerprint, activities.FingerprintInput{
		Cluster: cluster,
		Tables:  crashRecoveryTables,
	}).Get(ctx, &after); err != nil {
		return err
	}

	for _, table := range crashRecoveryTables {
		if before[table] != after[table] {
			return fmt.Errorf("data changed across crash recovery: table %q fingerprint %s != baseline %s",
				table, after[table], before[table])
		}
	}

	return nil
}
