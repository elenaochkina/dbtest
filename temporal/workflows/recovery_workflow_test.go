package workflows

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/probe"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/activities"
)

// newRecoveryEnvironment returns a test environment whose recovery activities
// succeed, except Disrupt, which returns disruptError. The returned counter
// holds the number of StopProbe calls.
func newRecoveryEnvironment(t *testing.T, disruptError error) (*testsuite.TestWorkflowEnvironment, *int) {
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	stopProbeCalls := 0
	register := func(name string, function any) {
		environment.RegisterActivityWithOptions(function, activity.RegisterOptions{Name: name})
	}
	register("CheckSupported", func(context.Context, activities.CheckSupportedInput) error { return nil })
	register("StartRun", func(context.Context, activities.StartRunInput) (uuid.UUID, error) { return uuid.New(), nil })
	register("EndRun", func(context.Context, activities.EndRunInput) error { return nil })
	register("Provision", func(context.Context, activities.ProvisionInput) (provider.ClusterInfo, error) {
		return provider.ClusterInfo{}, nil
	})
	register("WaitForReady", func(context.Context, activities.WaitForReadyInput) error { return nil })
	register("Deprovision", func(context.Context, activities.DeprovisionInput) error { return nil })
	register("InitializeBenchContainer", func(context.Context, activities.BenchContainerInput) error { return nil })
	register("StartProbe", func(context.Context, activities.ProbeInput) (harness.Handle, error) { return harness.Handle{}, nil })
	register("CheckProbeReadiness", func(context.Context, activities.ProbeReadyInput) (bool, error) { return true, nil })
	register("Disrupt", func(context.Context, activities.DisruptInput) (provider.ClusterInfo, error) {
		return provider.ClusterInfo{}, disruptError
	})
	register("StopProbe", func(context.Context, activities.ContainerInput) (probe.Result, error) {
		stopProbeCalls++
		return probe.Result{}, nil
	})
	register("SaveDowntimeResults", func(context.Context, activities.SaveDowntimeInput) error { return nil })
	return environment, &stopProbeCalls
}

func TestRecoveryStopsProbeOnceOnSuccess(t *testing.T) {
	environment, stopProbeCalls := newRecoveryEnvironment(t, nil)
	environment.ExecuteWorkflow(RecoveryWorkflow, RecoveryWorkflowConfig{Provider: provider.Docker, Disruption: provider.Restart, Repetitions: 2})
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if *stopProbeCalls != 1 {
		t.Fatalf("StopProbe called %d times, want 1", *stopProbeCalls)
	}
}

func TestRecoveryStopsProbeWhenDisruptFails(t *testing.T) {
	environment, stopProbeCalls := newRecoveryEnvironment(t, errors.New("disrupt failed"))
	environment.ExecuteWorkflow(RecoveryWorkflow, RecoveryWorkflowConfig{Provider: provider.Docker, Disruption: provider.Restart, Repetitions: 2})
	if err := environment.GetWorkflowError(); err == nil {
		t.Fatal("workflow succeeded, want the disrupt error")
	}
	if *stopProbeCalls != 1 {
		t.Fatalf("StopProbe called %d times, want 1", *stopProbeCalls)
	}
}
