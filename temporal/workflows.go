package temporal

import (
	"github.com/elenaochkina/dbtest/provider"
	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"
)

// ProvisionTeardownWorkflow provisions a cluster and guarantees teardown.
func ProvisionTeardownWorkflow(ctx workflow.Context, cfg Config) error {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	var a *Activities // typed-nil: the SDK reads only the method NAME off a.Provision

	type provisionIdentity struct{ Token, Password string }
	var id provisionIdentity
	if err := workflow.SideEffect(ctx, func(workflow.Context) any {
		return provisionIdentity{
			Token:    uuid.NewString(),
			Password: uuid.NewString(),
		}
	}).Get(&id); err != nil {
		return err
	}

	var cluster provider.ClusterInfo
	if err := workflow.ExecuteActivity(ctx, a.Provision, ProvisionInput{
		Provider: cfg.Provider,
		Request:  cfg.Request,
		Token:    id.Token,
		Password: id.Password,
	}).Get(ctx, &cluster); err != nil {
		return err // provision failed → nothing created → nothing to tear down
	}

	// Durable teardown. Registered only after provision succeeds, and run on a
	// disconnected context so a cancelled/failed workflow still cleans up.
	defer func() {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		dctx = workflow.WithActivityOptions(dctx, defaultActivityOptions)
		_ = workflow.ExecuteActivity(dctx, a.Deprovision, DeprovisionInput{
			Provider:  cfg.Provider,
			ClusterID: cluster.ID,
		}).Get(dctx, nil)
	}()

	// Wait for the cluster to accept connections. Runs after teardown is
	// registered, so a readiness failure still deprovisions the instance.
	if err := workflow.ExecuteActivity(ctx, a.WaitForReady, WaitForReadyInput{
		Provider: cfg.Provider,
		Cluster:  cluster,
	}).Get(ctx, nil); err != nil {
		return err
	}

	return nil
}
