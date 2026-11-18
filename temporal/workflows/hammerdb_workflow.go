package workflows

import (
	"fmt"
	"time"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/activities"
	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type HammerDBWorkflowConfig struct {
	Cloud        provider.CloudName
	Provider     provider.DBServiceName
	Request      provider.ProvisionRequest
	Warehouses   int
	VirtualUsers int
	RampUp       time.Duration
	Duration     time.Duration // the measured part of each run
	Runs         int
	BuildTimeout time.Duration
}

// HammerDBWorkflow measures TPROC-C throughput. It builds the schema once, then
// runs the timed driver cfg.Runs times and records one row per run.
func HammerDBWorkflow(ctx workflow.Context, cfg HammerDBWorkflowConfig) (err error) {
	if cfg.Runs < 1 || cfg.Warehouses < 1 || cfg.VirtualUsers < 1 || cfg.Duration < time.Minute || cfg.BuildTimeout <= 0 {
		return temporal.NewNonRetryableApplicationError(
			"hammerdb needs runs, warehouses and virtual users of at least 1, a duration of at least 1 minute, and a build timeout",
			"InvalidConfig", nil)
	}
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	var (
		runs *activities.StateDBActivities
		prov *activities.ProviderActivities
		harn *activities.HarnessActivities
	)

	if err = workflow.ExecuteActivity(ctx, prov.CheckSupported, activities.CheckSupportedInput{
		Cloud:    cfg.Cloud,
		Provider: cfg.Provider,
		Request:  cfg.Request,
	}).Get(ctx, nil); err != nil {
		return err
	}

	var runner activities.ResolvedRunner
	if err = workflow.ExecuteActivity(ctx, harn.ResolveRunner, activities.RunnerInput{
		Cloud: cfg.Cloud,
	}).Get(ctx, &runner); err != nil {
		return err
	}

	var runID uuid.UUID
	if err = workflow.ExecuteActivity(ctx, runs.StartRun, activities.StartRunInput{
		Scenario: "hammerdb",
		Cloud:    cfg.Cloud,
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

	clusterID := "dbtest-" + runID.String()
	defer func() {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		dctx = workflow.WithActivityOptions(dctx, defaultActivityOptions)
		if derr := workflow.ExecuteActivity(dctx, prov.Deprovision, activities.DeprovisionInput{
			Cloud:     cfg.Cloud,
			Provider:  cfg.Provider,
			ClusterID: clusterID,
		}).Get(dctx, nil); derr != nil && err == nil {
			err = derr
		}
	}()

	var cluster provider.ClusterInfo
	if err = workflow.ExecuteActivity(ctx, prov.Provision, activities.ProvisionInput{
		Cloud:    cfg.Cloud,
		Provider: cfg.Provider,
		Request:  cfg.Request,
		Token:    clusterID,
		Password: password,
	}).Get(ctx, &cluster); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(ctx, prov.WaitForReady, activities.WaitForReadyInput{
		Cloud:    cfg.Cloud,
		Provider: cfg.Provider,
		Cluster:  cluster,
	}).Get(ctx, nil); err != nil {
		return err
	}

	bench := activities.BenchContainerInput{
		Cloud:      cfg.Cloud,
		Runner:     runner.Name,
		Image:      runner.Images.Bench,
		DSN:        cluster.PrivateConnInfo.URL(cluster.Password),
		Workload:   "tpcc",
		Warehouses: cfg.Warehouses,
		Clients:    cfg.VirtualUsers,
		RampUp:     cfg.RampUp,
		Duration:   cfg.Duration,
	}

	build := bench
	build.Init = true
	build.Name = "dbtest-tpcc-build-" + runID.String()
	built, err := runBench(ctx, harn, build, cfg.BuildTimeout)
	if err != nil {
		return err
	}

	for repetition := 1; repetition <= cfg.Runs; repetition++ {
		run := bench
		run.Name = fmt.Sprintf("dbtest-tpcc-run-%d-%s", repetition, runID)
		result, err := runBench(ctx, harn, run, cfg.RampUp+cfg.Duration+15*time.Minute)
		if err != nil {
			return err
		}
		if err = workflow.ExecuteActivity(ctx, runs.SaveTPCCResult, activities.SaveTPCCInput{
			RunID:            runID,
			Provider:         cfg.Provider,
			HighAvailability: cfg.Request.HighAvailability,
			Repetition:       repetition,
			Bench:            run,
			Build:            built,
			Run:              result,
		}).Get(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

// runBench starts a bench container and waits for its result. A failed wait
// stops the container before returning.
func runBench(ctx workflow.Context, harn *activities.HarnessActivities, input activities.BenchContainerInput, timeout time.Duration) (activities.BenchResult, error) {
	startCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 5},
	})
	var handle harness.Handle
	if err := workflow.ExecuteActivity(startCtx, harn.StartBench, input).Get(ctx, &handle); err != nil {
		return activities.BenchResult{}, err
	}
	container := activities.ContainerInput{Cloud: input.Cloud, Runner: input.Runner, Handle: handle}

	waitCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: timeout,
		HeartbeatTimeout:    time.Minute,
		WaitForCancellation: true,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 5},
	})
	var result activities.BenchResult
	err := workflow.ExecuteActivity(waitCtx, harn.WaitForBenchResult, container).Get(ctx, &result)
	if err != nil {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		dctx = workflow.WithActivityOptions(dctx, defaultActivityOptions)
		_ = workflow.ExecuteActivity(dctx, harn.StopBench, container).Get(dctx, nil)
		return activities.BenchResult{}, err
	}
	return result, nil
}
