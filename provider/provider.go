package provider

import (
	"context"
	"fmt"
	"sort"

	"github.com/elenaochkina/dbtest/telemetry"
)

// PGTarget is provider-agnostic connection info for a Postgres instance.
type PGTarget struct {
	Host     string
	Port     int
	Database string
	User     string
}

// Addr returns the "host:port" pair.
func (t PGTarget) Addr() string {
	return fmt.Sprintf("%s:%d", t.Host, t.Port)
}

// URL renders a pgx/libpq URL DSN with the password injected. Build it only at
// the moment of connecting, so the secret never has to live alongside the target.
func (t PGTarget) URL(password string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s", t.User, password, t.Host, t.Port, t.Database)
}

// ClusterInfo is returned by Provision and used to connect and deprovision.
type ClusterInfo struct {
	ID       string   // provider-specific identifier (e.g. Docker container ID, RDS instance ID)
	Target   PGTarget // how the worker reaches the database
	Internal PGTarget // a sibling container reaches the same database.
	Password string   // master password; rendered into a DSN at connect time, never persisted
}

type ProvisionRequest struct {
	VCPU            float64
	MemoryMiB       int
	DiskGiB         int
	PostgresVersion string
}

// Provider is the interface every database provider must satisfy.

type Provider interface {
	Provision(ctx context.Context, req ProvisionRequest, token, password string) (ClusterInfo, error)
	WaitForReady(ctx context.Context, cluster ClusterInfo) error
	Deprovision(ctx context.Context, clusterID string) error
}

// FailureInjector is an optional provider capability: providers that can inject
// a forced, ungraceful failure into a running cluster implement it.
type FailureInjector interface {
	KillProcess(ctx context.Context, cluster ClusterInfo) (ClusterInfo, error)
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

// Call this from init() in each provider package.
func Register(name ProviderName, fn func(*telemetry.Telemetry) (Provider, error)) {
	registry[name] = fn
}

// Run returns a Provider for the given name.
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
