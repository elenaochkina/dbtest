package temporal

import (
	"time"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/workload"
)

// Config is the workflow input for every scenario — the durable, serializable
// equivalent of scenario.Config.
type Config struct {
	Provider    provider.ProviderName
	Request     provider.ProvisionRequest // cluster resource spec the caller declares
	Seed        int64
	Warehouses  int
	ScaleFactor int
	Clients     int
	Duration    time.Duration
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
// workload parameters. The DSN is built from Cluster at run time, so the
// password is never a separate field here.
type WorkloadInput struct {
	Cluster provider.ClusterInfo
	Config  workload.Config
}

type DeprovisionInput struct {
	Provider  provider.ProviderName
	ClusterID string
}
