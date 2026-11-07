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

// Descriptor describes a database service without building it.
type Descriptor struct {
	Name                     provider.DBServiceName
	Clouds                   []provider.CloudName
	SupportsHighAvailability bool
	Disruptions              func(provider.ProvisionRequest) []provider.Disruption
	BuildDBService           func(cloud.Cloud, *telemetry.Telemetry) (DBService, error)
}

var registry = map[provider.DBServiceName]Descriptor{}

// Register adds a database service and records the clouds it runs in. Call it
// from init() in the service's package.
func Register(descriptor Descriptor) {
	registry[descriptor.Name] = descriptor
	for _, name := range descriptor.Clouds {
		cloud.Host(name, descriptor.Name)
	}
}

func Get(name provider.DBServiceName) (Descriptor, bool) {
	descriptor, ok := registry[name]
	return descriptor, ok
}

// Names returns the registered services, sorted.
func Names() []provider.DBServiceName {
	return slices.Sorted(maps.Keys(registry))
}

// Disruptions returns the disruptions the named service supports for req, or
// none if it is not registered.
func Disruptions(name provider.DBServiceName, req provider.ProvisionRequest) []provider.Disruption {
	descriptor, ok := registry[name]
	if !ok {
		return nil
	}
	return descriptor.Disruptions(req)
}

// NewDBService builds the named service in cloud. It fails if the service does not run in cloud.
func NewDBService(name provider.DBServiceName, c cloud.Cloud, tel *telemetry.Telemetry) (DBService, error) {
	descriptor, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown database service %q; registered: %v", name, Names())
	}
	if !c.Supports(name) {
		return nil, fmt.Errorf("%s does not run in %s", name, c.Name())
	}
	return descriptor.BuildDBService(c, tel)
}
