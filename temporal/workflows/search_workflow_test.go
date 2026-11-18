package workflows

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/elenaochkina/dbtest/experiment/search"
	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/activities"
	"github.com/stretchr/testify/mock"
)

var searchConfig = SearchWorkflowConfig{
	Cloud:      provider.CloudName("local"),
	Provider:   provider.Docker,
	Tool:       "hammerdb",
	Warehouses: 32,
	Scale:      200,
	RampUp:     time.Minute,
	Duration:   3 * time.Minute,
	Search: search.Config{
		MinClients: 8, MaxClients: 128, Resolution: 4, MaxTrials: 12, Tolerance: 0.03, Threshold: 0.95,
	},
	BuildTimeout: time.Hour,
}

func TestSearchWorkflowBuildsThenSearches(t *testing.T) {
	for tool, workload := range map[string]string{"hammerdb": "tpcc", "pgbench": "pgbench"} {
		t.Run(tool, func(t *testing.T) {
			runner := activities.ResolvedRunner{Name: harness.Docker, Images: harness.Images{Bench: "bench", Tester: "tester"}}
			env := databaseEnv(runner)
			var started []activities.BenchContainerInput
			env.OnActivity("StartBench", mock.Anything, mock.Anything).Return(
				func(_ context.Context, input activities.BenchContainerInput) (harness.Handle, error) {
					started = append(started, input)
					return harness.Handle{ID: input.Name, SelfExits: true}, nil
				})
			env.OnActivity("WaitForBenchResult", mock.Anything, mock.Anything).Return(
				func(_ context.Context, input activities.ContainerInput) (activities.BenchResult, error) {
					if strings.Contains(input.Handle.ID, "-build-") {
						return activities.BenchResult{Phase: "init", ElapsedS: 90}, nil
					}
					return activities.BenchResult{Phase: "search",
						Metrics: map[string]float64{"peak_clients": 16},
						Trials:  []activities.BenchTrial{{Clients: 8}, {Clients: 16}, {Clients: 16, Repeat: true}},
					}, nil
				})
			var saved activities.SaveSearchInput
			env.OnActivity("SaveSearch", mock.Anything, mock.Anything).Return(
				func(_ context.Context, input activities.SaveSearchInput) error {
					saved = input
					return nil
				})

			cfg := searchConfig
			cfg.Tool = tool
			env.ExecuteWorkflow(SearchWorkflow, cfg)
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			if len(started) != 2 {
				t.Fatalf("started %d containers, want the build then the search", len(started))
			}
			build, tester := started[0], started[1]
			if build.Image != "bench" || !build.Init || build.Workload != workload || build.Scale != 200 {
				t.Errorf("build = %+v, want the bench image building %s", build, workload)
			}
			if tester.Image != "tester" || tester.Tool != tool || tester.Search == nil {
				t.Errorf("tester = %+v, want the tester image searching with %s", tester, tool)
			}
			if saved.Build.ElapsedS != 90 || len(saved.Result.Trials) != 3 || saved.Search.Tool != tool {
				t.Errorf("saved = %+v", saved)
			}
			env.AssertCalled(t, "Deprovision", mock.Anything, mock.Anything)
		})
	}
}

func TestSearchWorkflowRejectsInvalidConfig(t *testing.T) {
	invalid := map[string]func(*SearchWorkflowConfig){
		"threshold above 1":     func(cfg *SearchWorkflowConfig) { cfg.Search.Threshold = 1.5 },
		"unknown tool":          func(cfg *SearchWorkflowConfig) { cfg.Tool = "sysbench" },
		"pgbench without scale": func(cfg *SearchWorkflowConfig) { cfg.Tool, cfg.Scale = "pgbench", 0 },
	}
	for name, change := range invalid {
		t.Run(name, func(t *testing.T) {
			env := hammerDBEnv(t)
			cfg := searchConfig
			change(&cfg)
			env.ExecuteWorkflow(SearchWorkflow, cfg)
			if env.GetWorkflowError() == nil {
				t.Fatal("workflow accepted the config")
			}
			env.AssertNotCalled(t, "Provision", mock.Anything, mock.Anything)
		})
	}
}
