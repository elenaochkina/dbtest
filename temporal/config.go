// Package temporal is the only package that imports the Temporal SDK. It holds
// the durable workflows (scenario orchestration), the activities (the
// side-effecting step logic), and the config that binds them. E
package temporal

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const TaskQueue = "dbtest-tq"

// defaultActivityOptions apply to every activity a workflow schedules.
var defaultActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 20 * time.Minute,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts: 3,
	},
}
