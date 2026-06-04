package provider

import "context"

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
