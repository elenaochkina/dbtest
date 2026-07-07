package temporal

import (
	"time"

	"github.com/elenaochkina/dbtest/provider"
)

// Config is the workflow input for every scenario — the durable, serializable
// equivalent of scenario.Config.
type Config struct {
	Provider    provider.ProviderName
	Seed        int64
	Warehouses  int
	ScaleFactor int
	Clients     int
	Duration    time.Duration
}

type ProvisionInput struct {
	Provider provider.ProviderName
	Request  provider.ProvisionRequest
}

type DeprovisionInput struct {
	Provider  provider.ProviderName
	ClusterID string
}
