package provider

import (
	"context"
	"fmt"
	"sort"

	"github.com/elenaochkina/dbtest/telemetry"
)

// ClusterInfo is returned by Provision and used to connect and deprovision.
type ClusterInfo struct {
	ID  string // provider-specific identifier (e.g. Docker container ID, RDS instance ID)
	DSN string // postgres connection string, e.g. "postgres://user:pass@host:port/db"
}

// Provider is the interface every database provider must satisfy.
type Provider interface {
	Provision(ctx context.Context) (ClusterInfo, error)
	WaitForReady(ctx context.Context, cluster ClusterInfo) error
	Deprovision(ctx context.Context, clusterID string) error
}

// Restarter is an optional provider capability: providers that can restart a
// running cluster in place implement it. Restart must be an ungraceful, forced
// restart — it kills the database process abruptly (no clean shutdown) and
// brings it back, so the cluster comes up through crash recovery. 
type Restarter interface {
	Restart(ctx context.Context, cluster ClusterInfo) (ClusterInfo, error)
}

// ProviderName is the typed identifier for a provider implementation.
type ProviderName string

const (
	Docker ProviderName = "docker"
	AWS    ProviderName = "aws"
)

// registry maps provider names to constructor functions.
// Populated by each provider package via init() + Register().
var registry = map[ProviderName]func(*telemetry.Telemetry) (Provider, error){}

// Register adds a provider constructor to the registry.
// Call this from init() in each provider package.
func Register(name ProviderName, fn func(*telemetry.Telemetry) (Provider, error)) {
	registry[name] = fn
}

// Run returns a Provider for the given name.
// tel may be nil — metrics and logs are skipped when nil.
func Run(name ProviderName, tel *telemetry.Telemetry) (Provider, error) {
	fn, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q; registered: %v", name, registeredNames())
	}
	return fn(tel)
}

func registeredNames() []string {
	names := make([]string, 0, len(registry))
	for k := range registry {
		names = append(names, string(k))
	}
	sort.Strings(names)
	return names
}
