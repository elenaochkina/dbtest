package workflows

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/activities"
	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

func hammerDBEnv(t *testing.T) *testsuite.TestWorkflowEnvironment {
	env := databaseEnv(activities.ResolvedRunner{Name: harness.Docker})
	env.OnActivity("StartBench", mock.Anything, mock.Anything).Return(harness.Handle{ID: "c1", SelfExits: true}, nil)
	return env
}

// databaseEnv mocks every activity of withDatabase.
func databaseEnv(runner activities.ResolvedRunner) *testsuite.TestWorkflowEnvironment {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivity(&activities.StateDBActivities{})
	env.RegisterActivity(&activities.ProviderActivities{})
	env.RegisterActivity(&activities.HarnessActivities{})

	env.OnActivity("CheckSupported", mock.Anything, mock.Anything).Return(nil)
	env.OnActivity("ResolveRunner", mock.Anything, mock.Anything).Return(runner, nil)
	env.OnActivity("StartRun", mock.Anything, mock.Anything).Return(uuid.New(), nil)
	env.OnActivity("EndRun", mock.Anything, mock.Anything).Return(nil)
	env.OnActivity("Provision", mock.Anything, mock.Anything).Return(provider.ClusterInfo{}, nil)
	env.OnActivity("WaitForReady", mock.Anything, mock.Anything).Return(nil)
	env.OnActivity("Deprovision", mock.Anything, mock.Anything).Return(nil)
	return env
}

var hammerDBConfig = HammerDBWorkflowConfig{
	Cloud:        provider.CloudName("local"),
	Provider:     provider.Docker,
	Warehouses:   2,
	VirtualUsers: 4,
	Duration:     time.Minute,
	Runs:         3,
	BuildTimeout: time.Hour,
}

func TestHammerDBWorkflowSavesEachRun(t *testing.T) {
	env := hammerDBEnv(t)
	env.OnActivity("WaitForBenchResult", mock.Anything, mock.Anything).Return(activities.BenchResult{
		Metrics: map[string]float64{"nopm": 1000, "tpm": 2200},
	}, nil)
	var saved []int
	env.OnActivity("SaveTPCCResult", mock.Anything, mock.Anything).Return(
		func(_ context.Context, input activities.SaveTPCCInput) error {
			saved = append(saved, input.Repetition)
			return nil
		})

	env.ExecuteWorkflow(HammerDBWorkflow, hammerDBConfig)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 3 || saved[0] != 1 || saved[2] != 3 {
		t.Errorf("saved repetitions = %v, want [1 2 3]", saved)
	}
	env.AssertNotCalled(t, "StopBench", mock.Anything, mock.Anything)
	env.AssertNumberOfCalls(t, "StartBench", 4)
	env.AssertCalled(t, "Deprovision", mock.Anything, mock.Anything)
}

func TestHammerDBWorkflowStopsBenchOnFailedWait(t *testing.T) {
	env := hammerDBEnv(t)
	env.OnActivity("WaitForBenchResult", mock.Anything, mock.Anything).Return(activities.BenchResult{}, errors.New("bench failed"))
	env.OnActivity("StopBench", mock.Anything, mock.Anything).Return(nil)

	env.ExecuteWorkflow(HammerDBWorkflow, hammerDBConfig)
	if env.GetWorkflowError() == nil {
		t.Fatal("workflow succeeded with a failed bench")
	}
	env.AssertCalled(t, "StopBench", mock.Anything, mock.Anything)
	env.AssertCalled(t, "Deprovision", mock.Anything, mock.Anything)
}

func TestHammerDBWorkflowRejectsConfig(t *testing.T) {
	env := hammerDBEnv(t)
	cfg := hammerDBConfig
	cfg.Duration = 30 * time.Second
	env.ExecuteWorkflow(HammerDBWorkflow, cfg)
	if env.GetWorkflowError() == nil {
		t.Fatal("workflow accepted a 30s duration")
	}
}
