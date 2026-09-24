package workflows

import (
	"fmt"
	"time"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/probe"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/activities"
	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type RecoveryWorkflowConfig struct {
	Provider    provider.ProviderName
	Request     provider.ProvisionRequest
	Disruption  provider.Disruption
	Repetitions int
	// Settle is how long to wait after each disruption before the next one,
	Settle time.Duration

	ProbeImage string
	BenchImage string
	Scale      int
	// Probe samples frequency
	ProbeInterval time.Duration
}

// onceOnly is for activities a retry would corrupt: disrupting twice is two
// disruptions, and stopping a container twice finds it already gone.
var onceOnly = workflow.ActivityOptions{
	StartToCloseTimeout: 20 * time.Minute,
	RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
}

// RecoveryWorkflow measures how long a database is unavailable across repeate disruptions.
// It seeds the database, starts a prober, disrupts N times, then
// stops the prober and records one row per disruption.
func RecoveryWorkflow(ctx workflow.Context, cfg RecoveryWorkflowConfig) (err error) {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	var (
		runs *activities.StateDBActivities
		prov *activities.ProviderActivities
		harn *activities.HarnessActivities
	)

	runner, err := runnerFor(cfg.Provider)
	if err != nil {
		return err
	}

	// Cheapest possible failure: before anything is provisioned.
	if err = workflow.ExecuteActivity(ctx, prov.CheckSupported, activities.CheckSupportedInput{
		Provider:   cfg.Provider,
		Request:    cfg.Request,
		Disruption: cfg.Disruption,
	}).Get(ctx, nil); err != nil {
		return err
	}

	var runID uuid.UUID
	if err = workflow.ExecuteActivity(ctx, runs.StartRun, activities.StartRunInput{
		Scenario: "recovery",
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

	// Seeding finishes before the prober starts
	if err = workflow.ExecuteActivity(ctx, harn.InitializeBenchContainer, activities.BenchContainerInput{
		Runner:   runner,
		Image:    cfg.BenchImage,
		Name:     "dbtest-seed-" + runID.String(),
		DSN:      cluster.Internal.URL(cluster.Password),
		Workload: "pgbench",
		Scale:    cfg.Scale,
	}).Get(ctx, nil); err != nil {
		return err
	}

	var probeHandle harness.Handle
	if err = workflow.ExecuteActivity(ctx, harn.StartProbe, activities.ProbeInput{
		Runner:   runner,
		Image:    cfg.ProbeImage,
		Name:     "dbtest-probe-" + runID.String(),
		DSN:      cluster.Internal.URL(cluster.Password),
		Interval: cfg.ProbeInterval,
	}).Get(ctx, &probeHandle); err != nil {
		return err
	}
	// On the happy path the container is already gone and stopping it again is a no-op.
	defer func() {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		dctx = workflow.WithActivityOptions(dctx, onceOnly)
		_ = workflow.ExecuteActivity(dctx, harn.StopProbe, activities.ContainerInput{
			Runner: runner,
			Handle: probeHandle,
		}).Get(dctx, nil)
	}()

	// A baseline before the first disruption.
	// robe needs a few successful samples to establish lastOK before the first disruption has something to measure from.
	if err = workflow.Sleep(ctx, cfg.Settle); err != nil {
		return err
	}

	for i := 0; i < cfg.Repetitions; i++ {
		octx := workflow.WithActivityOptions(ctx, onceOnly)
		if err = workflow.ExecuteActivity(octx, prov.Disrupt, activities.DisruptInput{
			Provider:   cfg.Provider,
			Cluster:    cluster,
			Disruption: cfg.Disruption,
			// Disrupt returns refreshed connection info: a restarted container
			// comes back on a different port.
		}).Get(ctx, &cluster); err != nil {
			return err
		}
		if err = workflow.ExecuteActivity(ctx, prov.WaitForReady, activities.WaitForReadyInput{
			Provider: cfg.Provider,
			Cluster:  cluster,
		}).Get(ctx, nil); err != nil {
			return err
		}
		if err = workflow.Sleep(ctx, cfg.Settle); err != nil {
			return err
		}
	}

	// Stopped here rather than in a defer: the result feeds SaveDowntimeResults,
	// and a defer runs too late to hand it anywhere.
	var result probe.Result
	octx := workflow.WithActivityOptions(ctx, onceOnly)
	if err = workflow.ExecuteActivity(octx, harn.StopProbe, activities.ContainerInput{
		Runner: runner,
		Handle: probeHandle,
	}).Get(ctx, &result); err != nil {
		return err
	}

	// Rows are numbered by outage, so they only describe the disruptions if the
	// two counts agree. Fewer means a disruption was too brief for the prober to
	// catch; more means something else interrupted the database. Either way every
	// row after the first mismatch is mislabelled.
	if got := len(result.Writable.Outages); got != cfg.Repetitions {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("observed %d outages, applied %d disruptions", got, cfg.Repetitions),
			"OutageCountMismatch", nil,
		)
	}

	return workflow.ExecuteActivity(ctx, runs.SaveDowntimeResults, activities.SaveDowntimeInput{
		RunID:      runID,
		Provider:   cfg.Provider,
		Disruption: cfg.Disruption,
		Result:     result,
	}).Get(ctx, nil)
}

// runnerFor pairs a provider with the place its containers run.
func runnerFor(p provider.ProviderName) (harness.RunnerName, error) {
	switch p {
	case provider.Docker:
		return harness.Docker, nil
	case provider.AWS:
		return harness.Fargate, nil
	default:
		return "", temporal.NewNonRetryableApplicationError(
			"no container runner for provider "+string(p), "UnknownProvider", nil)
	}
}
