// Package dbservice holds the database services dbtest provisions and disrupts.
package dbservice

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/google/uuid"
)

// DBService provisions, disrupts and deletes one kind of database.
type DBService interface {
	Provision(ctx context.Context, req provider.ProvisionRequest, token, password string) (provider.ClusterInfo, error)
	WaitForReady(ctx context.Context, cluster provider.ClusterInfo) error
	Deprovision(ctx context.Context, clusterID string) error

	// Disrupt applies the disruption and returns once the cluster has settled.
	// The returned ClusterInfo replaces the caller's copy.
	Disrupt(ctx context.Context, cluster provider.ClusterInfo, disruption provider.Disruption) (provider.ClusterInfo, error)

	// SwitchoverReadiness reports whether the cluster accepts the next disruption now.
	SwitchoverReadiness(ctx context.Context, cluster provider.ClusterInfo) (bool, error)
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

var registeredServices = map[provider.DBServiceName]Registration{}

// Register adds a database service and records the clouds it runs in. Call it
// from init() in the service's file.
func Register(registration Registration) {
	registeredServices[registration.Name()] = registration
	for _, name := range registration.Clouds() {
		cloud.RegisterService(name, registration.Name())
	}
}

// Get returns the named service's registration.
func Get(name provider.DBServiceName) (Registration, bool) {
	registration, ok := registeredServices[name]
	return registration, ok
}

// Names returns the registered services, sorted.
func Names() []provider.DBServiceName {
	return slices.Sorted(maps.Keys(registeredServices))
}

// Disruptions returns the disruptions the named service supports for req, or
// none if it is not registered.
func Disruptions(name provider.DBServiceName, req provider.ProvisionRequest) []provider.Disruption {
	registration, ok := registeredServices[name]
	if !ok {
		return nil
	}
	return registration.Disruptions(req)
}

// NewDBService builds the named service in cloud. It fails if the service does not run in cloud.
func NewDBService(name provider.DBServiceName, c cloud.Cloud, tel *telemetry.Telemetry) (DBService, error) {
	registration, ok := registeredServices[name]
	if !ok {
		return nil, fmt.Errorf("unknown database service %q; registered: %v", name, Names())
	}
	if !c.Supports(name) {
		return nil, fmt.Errorf("%s does not run in %s", name, c.Name())
	}
	return registration.Build(c, tel)
}

// waitForConnection runs the probe in ready mode on the cloud's default runner
// against the cluster's internal address and returns once the database answers.
func waitForConnection(ctx context.Context, c cloud.Cloud, tel *telemetry.Telemetry, cluster provider.ClusterInfo) error {
	name := c.Runners()[0]
	r, err := c.NewRunner(name, tel)
	if err != nil {
		return fmt.Errorf("runner %q: %w", name, err)
	}
	h, err := r.Run(ctx, harness.Spec{
		Image:     c.Images(name).Probe,
		Name:      "dbtest-ready-" + uuid.NewString(),
		Args:      []string{"-dsn", cluster.Internal.URL(cluster.Password), "-ready", "5m"},
		SelfExits: true,
	})
	if err != nil {
		return fmt.Errorf("start readiness check for %s: %w", cluster.ID, err)
	}
	if out, err := r.Stop(ctx, h); err != nil {
		return fmt.Errorf("cluster %s did not answer within 5m: %w: %s", cluster.ID, err, harness.Tail(out))
	}
	if tel != nil {
		tel.Logger.Info("cluster is ready", slog.String("cluster", cluster.ID))
	}
	return nil
}
