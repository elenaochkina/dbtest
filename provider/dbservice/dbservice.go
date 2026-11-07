// Package dbservice holds the database services dbtest provisions and disrupts.
package dbservice

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/telemetry"
)

// DBService provisions, disrupts and deletes one kind of database.
type DBService interface {
	Provision(ctx context.Context, req provider.ProvisionRequest, token, password string) (provider.ClusterInfo, error)
	WaitForReady(ctx context.Context, cluster provider.ClusterInfo) error
	Deprovision(ctx context.Context, clusterID string) error

	// Disrupt applies the disruption and returns once the cluster has settled.
	// The returned ClusterInfo replaces the caller's copy.
	Disrupt(ctx context.Context, cluster provider.ClusterInfo, disruption provider.Disruption) (provider.ClusterInfo, error)
}

// Registration describes a database service and builds it. Each service
// registers one from init(); reading it needs no credentials.
type Registration interface {
	Name() provider.DBServiceName

	// Clouds lists the clouds the service runs in.
	Clouds() []provider.CloudName

	// SupportsHighAvailability reports whether the service can run with a standby.
	SupportsHighAvailability() bool

	// Disruptions returns what the service can do to a cluster provisioned from req.
	Disruptions(req provider.ProvisionRequest) []provider.Disruption

	// Build builds the service in c.
	Build(c cloud.Cloud, tel *telemetry.Telemetry) (DBService, error)
}

var registry = map[provider.DBServiceName]Registration{}

// Register adds a database service and records the clouds it runs in. Call it
// from init() in the service's file.
func Register(registration Registration) {
	registry[registration.Name()] = registration
	for _, name := range registration.Clouds() {
		cloud.RegisterService(name, registration.Name())
	}
}

// Get returns the named service's registration.
func Get(name provider.DBServiceName) (Registration, bool) {
	registration, ok := registry[name]
	return registration, ok
}

// Names returns the registered services, sorted.
func Names() []provider.DBServiceName {
	return slices.Sorted(maps.Keys(registry))
}

// Disruptions returns the disruptions the named service supports for req, or
// none if it is not registered.
func Disruptions(name provider.DBServiceName, req provider.ProvisionRequest) []provider.Disruption {
	registration, ok := registry[name]
	if !ok {
		return nil
	}
	return registration.Disruptions(req)
}

// NewDBService builds the named service in cloud. It fails if the service does not run in cloud.
func NewDBService(name provider.DBServiceName, c cloud.Cloud, tel *telemetry.Telemetry) (DBService, error) {
	registration, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown database service %q; registered: %v", name, Names())
	}
	if !c.Supports(name) {
		return nil, fmt.Errorf("%s does not run in %s", name, c.Name())
	}
	return registration.Build(c, tel)
}
