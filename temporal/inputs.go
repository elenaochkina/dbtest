package temporal

import (
	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/workload"
	"github.com/google/uuid"
)

// PgBenchWorkflowConfig is the input to PgBenchWorkflow.
// It holds provisioning (Provider, Request) and the workload params. Provider is
// the single source of truth for the provider name; the workflow propagates it
// into Workload.ProviderName, so callers leave that field unset.
type PgBenchWorkflowConfig struct {
	Provider provider.ProviderName
	Request  provider.ProvisionRequest // cluster resource spec the caller declares
	Workload workload.Config
}

type ProvisionInput struct {
	Provider provider.ProviderName
	Request  provider.ProvisionRequest
	Token    string // pinned instance identity; stable across retries so Provision is idempotent
	Password string // pinned master password; stable across retries so an adopted instance still matches
}

type WaitForReadyInput struct {
	Provider provider.ProviderName
	Cluster  provider.ClusterInfo
}

// WorkloadInput drives a workload activity: which cluster to run against and the
// workload parameters.
type WorkloadInput struct {
	Cluster provider.ClusterInfo
	Config  workload.Config
}

// StartRunInput opens a runs row recording this benchmark execution.
type StartRunInput struct {
	Scenario string
	Seed     int64
	Provider provider.ProviderName
}

// SaveResultInput persists one pgbench result.
type SaveResultInput struct {
	RunID  uuid.UUID
	Result pgbench.Result
}

// EndRunInput closes a runs row, recording whether the run passed.
type EndRunInput struct {
	RunID  uuid.UUID
	Passed bool
}

type DeprovisionInput struct {
	Provider  provider.ProviderName
	ClusterID string
}
