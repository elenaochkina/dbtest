// Package worker builds the Temporal worker that cmd/worker and the console run.
package worker

import (
	"github.com/elenaochkina/dbtest/telemetry"
	dbtemporal "github.com/elenaochkina/dbtest/temporal"
	"github.com/elenaochkina/dbtest/temporal/activities"
	"github.com/elenaochkina/dbtest/temporal/workflows"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	sdkworker "go.temporal.io/sdk/worker"
)

// New returns a worker on the dbtest task queue with every workflow and
// activity registered. The activities use statePool and tel.
func New(temporalClient client.Client, statePool *pgxpool.Pool, tel *telemetry.Telemetry) sdkworker.Worker {
	w := sdkworker.New(temporalClient, dbtemporal.TaskQueue, sdkworker.Options{})
	w.RegisterWorkflow(workflows.PgBenchWorkflow)
	w.RegisterWorkflow(workflows.RecoveryWorkflow)
	w.RegisterActivity(activities.NewStateDBActivities(statePool, tel))
	w.RegisterActivity(activities.NewProviderActivities(tel))
	w.RegisterActivity(activities.NewWorkloadActivities(tel))
	w.RegisterActivity(activities.NewDurabilityActivities(tel))
	w.RegisterActivity(activities.NewHarnessActivities(tel))
	return w
}
