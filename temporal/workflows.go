package temporal

import (
	"github.com/elenaochkina/dbtest/provider"
	"go.temporal.io/sdk/workflow"
)

// ProvisionTeardownWorkflow provisions a cluster and guarantees teardown. It is
// the smallest end-to-end durable scenario: provision, then defer deprovision so
// the cluster is torn down on every exit path — success, failure, cancellation,
// or a worker crash and replay. The scenario body (workloads, fingerprints,
// kill-process) slots in where noted, and each addition is one more activity.
func ProvisionTeardownWorkflow(ctx workflow.Context, cfg Config) error {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)
	var a *Activities // typed-nil: the SDK reads only the method NAME off a.Provision

	var cluster provider.ClusterInfo
	if err := workflow.ExecuteActivity(ctx, a.Provision, ProvisionInput{
		Provider: cfg.Provider,
		Request:  benchmarkRequest(),
	}).Get(ctx, &cluster); err != nil {
		return err // provision failed → nothing created → nothing to tear down
	}

	// Durable teardown. Registered only after provision succeeds, and run on a
	// disconnected context so a cancelled/failed workflow still cleans up. This
	// is the crash-proof form of scenario.Run's teardown stack.
	defer func() {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		dctx = workflow.WithActivityOptions(dctx, defaultActivityOptions)
		_ = workflow.ExecuteActivity(dctx, a.Deprovision, DeprovisionInput{
			Provider:  cfg.Provider,
			ClusterID: cluster.ID,
		}).Get(dctx, nil)
	}()

	// --- scenario body goes here (RunWorkload, Fingerprint, KillProcess) ---

	return nil
}

// benchmarkRequest is the default resource spec until scenarios carry their own,
// mirroring scenario.benchmarkResources.
func benchmarkRequest() provider.ProvisionRequest {
	return provider.ProvisionRequest{
		VCPU:            2,
		MemoryMiB:       2048,
		PostgresVersion: "16",
	}
}
