package workflows

import (
	"time"

	"github.com/elenaochkina/dbtest/experiment/search"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/activities"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// SearchWorkflowConfig configures a client search with one benchmark tool.
type SearchWorkflowConfig struct {
	Cloud    provider.CloudName
	Provider provider.DBServiceName
	Request  provider.ProvisionRequest
	// Tool is hammerdb or pgbench.
	Tool string
	// Warehouses sizes the hammerdb schema and Scale the pgbench tables.
	Warehouses int
	Scale      int
	// RampUp and Duration apply to each timed run of the search.
	RampUp       time.Duration
	Duration     time.Duration
	Search       search.Config
	BuildTimeout time.Duration
}

// SearchWorkflow finds the client count with the most throughput on one
// database: TPROC-C NOPM with hammerdb, TPC-B-like TPS with pgbench. It builds
// the tool's tables with the bench image, runs the tester image's search, and
// records each timed run and the search result.
func SearchWorkflow(ctx workflow.Context, cfg SearchWorkflowConfig) error {
	if err := cfg.Search.Validate(); err != nil {
		return temporal.NewNonRetryableApplicationError(err.Error(), "InvalidConfig", nil)
	}
	sized := cfg.Tool == "hammerdb" && cfg.Warehouses >= 1 || cfg.Tool == "pgbench" && cfg.Scale >= 1
	if !sized || cfg.Duration < time.Minute || cfg.BuildTimeout <= 0 {
		return temporal.NewNonRetryableApplicationError(
			"search needs hammerdb with at least 1 warehouse or pgbench with a scale of at least 1, a duration of at least 1 minute, and a build timeout",
			"InvalidConfig", nil)
	}
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	var (
		runs *activities.StateDBActivities
		harn *activities.HarnessActivities
	)
	return withDatabase(ctx, cfg.Tool+"-search", cfg.Cloud, cfg.Provider, cfg.Request, func(db database) error {
		build := activities.BenchContainerInput{
			Cloud:      cfg.Cloud,
			Runner:     db.runner.Name,
			Image:      db.runner.Images.Bench,
			Name:       "dbtest-" + cfg.Tool + "-build-" + db.runID.String(),
			DSN:        db.cluster.PrivateConnInfo.URL(db.cluster.Password),
			Workload:   map[string]string{"hammerdb": "tpcc", "pgbench": "pgbench"}[cfg.Tool],
			Init:       true,
			Warehouses: cfg.Warehouses,
			Clients:    cfg.Warehouses,
			Scale:      cfg.Scale,
		}
		built, err := runBench(ctx, harn, build, cfg.BuildTimeout)
		if err != nil {
			return err
		}

		tester := activities.BenchContainerInput{
			Cloud:      cfg.Cloud,
			Runner:     db.runner.Name,
			Image:      db.runner.Images.Tester,
			Name:       "dbtest-" + cfg.Tool + "-search-" + db.runID.String(),
			DSN:        build.DSN,
			Tool:       cfg.Tool,
			Warehouses: cfg.Warehouses,
			Scale:      cfg.Scale,
			RampUp:     cfg.RampUp,
			Duration:   cfg.Duration,
			Search:     &cfg.Search,
		}
		// Each run, the final drift run included, takes maintenance, ramp-up,
		// duration and the tool's start and stop.
		perRun := cfg.RampUp + cfg.Duration + 8*time.Minute
		timeout := time.Duration(cfg.Search.MaxTrials+1)*perRun + 15*time.Minute
		result, err := runBench(ctx, harn, tester, timeout)
		if err != nil {
			return err
		}
		if result.Phase != "search" || len(result.Trials) == 0 {
			return temporal.NewNonRetryableApplicationError(
				"tester ended without a search result", "BenchFailed", nil)
		}

		return workflow.ExecuteActivity(ctx, runs.SaveSearch, activities.SaveSearchInput{
			RunID:            db.runID,
			Provider:         cfg.Provider,
			HighAvailability: cfg.Request.HighAvailability,
			Search:           tester,
			Build:            built,
			Result:           result,
		}).Get(ctx, nil)
	})
}
