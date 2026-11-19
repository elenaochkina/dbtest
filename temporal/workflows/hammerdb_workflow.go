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
func HammerDBWorkflow(ctx workflow.Context, cfg HammerDBWorkflowConfig) error {
	if cfg.Runs < 1 || cfg.Warehouses < 1 || cfg.VirtualUsers < 1 || cfg.Duration < time.Minute || cfg.BuildTimeout <= 0 {
		return temporal.NewNonRetryableApplicationError(
			"hammerdb needs runs, warehouses and virtual users of at least 1, a duration of at least 1 minute, and a build timeout",
			"InvalidConfig", nil)
	}
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	var (
		runs *activities.StateDBActivities
		harn *activities.HarnessActivities
	)
	return withDatabase(ctx, "hammerdb", cfg.Cloud, cfg.Provider, cfg.Request, func(db database) error {
		bench := activities.BenchContainerInput{
			Cloud:      cfg.Cloud,
			Runner:     db.runner.Name,
			Image:      db.runner.Images.Bench,
			DSN:        db.cluster.PrivateConnInfo.URL(db.cluster.Password),
			Workload:   "tpcc",
			Warehouses: cfg.Warehouses,
			Clients:    cfg.VirtualUsers,
			RampUp:     cfg.RampUp,
			Duration:   cfg.Duration,
		}

		build := bench
		build.Init = true
		build.Name = "dbtest-tpcc-build-" + db.runID.String()
		built, err := runBench(ctx, harn, build, cfg.BuildTimeout)
		if err != nil {
			return err
		}

		for repetition := 1; repetition <= cfg.Runs; repetition++ {
			run := bench
			run.Name = fmt.Sprintf("dbtest-tpcc-run-%d-%s", repetition, db.runID)
			result, err := runBench(ctx, harn, run, cfg.RampUp+cfg.Duration+15*time.Minute)
			if err != nil {
				return err
			}
			if err = workflow.ExecuteActivity(ctx, runs.SaveTPCCResult, activities.SaveTPCCInput{
				RunID:            db.runID,
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
	})
}

// database is a provisioned database, its run and the runner that reaches it.
type database struct {
	runID   uuid.UUID
	runner  activities.ResolvedRunner
	cluster provider.ClusterInfo
}

// withDatabase opens a run, provisions a database for it and waits until the
// database answers, then calls body. It deprovisions the database and closes
// the run afterwards, also when a step or body fails.
func withDatabase(ctx workflow.Context, scenario string, cloud provider.CloudName, service provider.DBServiceName,
	request provider.ProvisionRequest, body func(database) error) (err error) {
	var (
		runs *activities.StateDBActivities
		prov *activities.ProviderActivities
		harn *activities.HarnessActivities
	)

	if err = workflow.ExecuteActivity(ctx, prov.CheckSupported, activities.CheckSupportedInput{
		Cloud:    cloud,
		Provider: service,
		Request:  request,
	}).Get(ctx, nil); err != nil {
		return err
	}

	var runner activities.ResolvedRunner
	if err = workflow.ExecuteActivity(ctx, harn.ResolveRunner, activities.RunnerInput{
		Cloud: cloud,
	}).Get(ctx, &runner); err != nil {
		return err
	}

	var runID uuid.UUID
	if err = workflow.ExecuteActivity(ctx, runs.StartRun, activities.StartRunInput{
		Scenario: scenario,
		Cloud:    cloud,
		Provider: service,
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
			Cloud:     cloud,
			Provider:  service,
			ClusterID: clusterID,
		}).Get(dctx, nil); derr != nil && err == nil {
			err = derr
		}
	}()

	var cluster provider.ClusterInfo
	if err = workflow.ExecuteActivity(ctx, prov.Provision, activities.ProvisionInput{
		Cloud:    cloud,
		Provider: service,
		Request:  request,
		Token:    clusterID,
		Password: password,
	}).Get(ctx, &cluster); err != nil {
		return err
	}

	if err = workflow.ExecuteActivity(ctx, prov.WaitForReady, activities.WaitForReadyInput{
		Cloud:    cloud,
		Provider: service,
		Cluster:  cluster,
	}).Get(ctx, nil); err != nil {
		return err
	}

	err = body(database{runID: runID, runner: runner, cluster: cluster})
	return err
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
