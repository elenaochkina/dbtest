package workflows

import (
	"time"

	"github.com/elenaochkina/dbtest/hammerdb"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/activities"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type HammerDBSearchWorkflowConfig struct {
	Cloud      provider.CloudName
	Provider   provider.DBServiceName
	Request    provider.ProvisionRequest
	Warehouses int
	// RampUp and Duration apply to each timed run of the search.
	RampUp       time.Duration
	Duration     time.Duration
	Search       activities.SearchSettings
	BuildTimeout time.Duration
}

// HammerDBSearchWorkflow finds the client count with the most TPROC-C
// throughput on one database. It builds the schema with the bench image, runs
// the tester image's search, and records each timed run and the search result.
func HammerDBSearchWorkflow(ctx workflow.Context, cfg HammerDBSearchWorkflowConfig) error {
	search := cfg.Search
	if err := (hammerdb.SearchConfig{
		MinClients: search.MinClients,
		MaxClients: search.MaxClients,
		Resolution: search.Resolution,
		MaxProbes:  search.MaxProbes,
		Tolerance:  search.Tolerance,
		Threshold:  search.Threshold,
	}).Validate(); err != nil {
		return temporal.NewNonRetryableApplicationError(err.Error(), "InvalidConfig", nil)
	}
	if cfg.Warehouses < 1 || cfg.Duration < time.Minute || cfg.BuildTimeout <= 0 {
		return temporal.NewNonRetryableApplicationError(
			"hammerdb search needs warehouses of at least 1, a duration of at least 1 minute, and a build timeout",
			"InvalidConfig", nil)
	}
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	var (
		runs *activities.StateDBActivities
		harn *activities.HarnessActivities
	)
	return withDatabase(ctx, "hammerdb-search", cfg.Cloud, cfg.Provider, cfg.Request, func(db database) error {
		build := activities.BenchContainerInput{
			Cloud:      cfg.Cloud,
			Runner:     db.runner.Name,
			Image:      db.runner.Images.Bench,
			Name:       "dbtest-tpcc-build-" + db.runID.String(),
			DSN:        db.cluster.PrivateConnInfo.URL(db.cluster.Password),
			Workload:   "tpcc",
			Init:       true,
			Warehouses: cfg.Warehouses,
			Clients:    cfg.Warehouses,
		}
		built, err := runBench(ctx, harn, build, cfg.BuildTimeout)
		if err != nil {
			return err
		}

		tester := activities.BenchContainerInput{
			Cloud:      cfg.Cloud,
			Runner:     db.runner.Name,
			Image:      db.runner.Images.Tester,
			Name:       "dbtest-tpcc-search-" + db.runID.String(),
			DSN:        build.DSN,
			Workload:   "tpcc",
			Warehouses: cfg.Warehouses,
			RampUp:     cfg.RampUp,
			Duration:   cfg.Duration,
			Search:     &search,
		}
		// Each run, the final drift run included, takes maintenance, ramp-up,
		// duration and HammerDB's start and stop.
		perRun := cfg.RampUp + cfg.Duration + 8*time.Minute
		timeout := time.Duration(search.MaxProbes+1)*perRun + 15*time.Minute
		result, err := runBench(ctx, harn, tester, timeout)
		if err != nil {
			return err
		}
		if result.Phase != "search" || len(result.Probes) == 0 {
			return temporal.NewNonRetryableApplicationError(
				"tester ended without a search result", "BenchFailed", nil)
		}

		return workflow.ExecuteActivity(ctx, runs.SaveTPCCSearch, activities.SaveTPCCSearchInput{
			RunID:            db.runID,
			Provider:         cfg.Provider,
			HighAvailability: cfg.Request.HighAvailability,
			Search:           tester,
			Build:            built,
			Result:           result,
		}).Get(ctx, nil)
	})
}
