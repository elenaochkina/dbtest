// Package activities holds the side-effecting step logic the workflows schedule.
package activities

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/provider/dbservice"
	"github.com/elenaochkina/dbtest/telemetry"
	"go.temporal.io/sdk/temporal"
)

type ProvisionInput struct {
	Cloud    provider.CloudName
	Provider provider.DBServiceName
	Request  provider.ProvisionRequest
	Token    string
	Password string
}

type WaitForReadyInput struct {
	Cloud    provider.CloudName
	Provider provider.DBServiceName
	Cluster  provider.ClusterInfo
}

type DeprovisionInput struct {
	Cloud     provider.CloudName
	Provider  provider.DBServiceName
	ClusterID string
}

type DisruptInput struct {
	Cloud      provider.CloudName
	Provider   provider.DBServiceName
	Cluster    provider.ClusterInfo
	Disruption provider.Disruption
}

// SwitchoverReadinessInput names the cluster to check.
type SwitchoverReadinessInput struct {
	Cloud    provider.CloudName
	Provider provider.DBServiceName
	Cluster  provider.ClusterInfo
}

type CheckSupportedInput struct {
	Cloud      provider.CloudName
	Provider   provider.DBServiceName
	Request    provider.ProvisionRequest
	Disruption provider.Disruption
}

// ProviderActivities own the cluster lifecycle — provision, wait for ready, and
// tear down — through a provider.
type ProviderActivities struct {
	tel *telemetry.Telemetry
}

func NewProviderActivities(tel *telemetry.Telemetry) *ProviderActivities {
	return &ProviderActivities{tel: tel}
}

func (a *ProviderActivities) Provision(ctx context.Context, input ProvisionInput) (provider.ClusterInfo, error) {
	p, err := a.service(input.Cloud, input.Provider)
	if err != nil {
		return provider.ClusterInfo{}, err
	}
	cluster, err := p.Provision(ctx, input.Request, input.Token, input.Password)
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("provision: %w", err)
	}
	return cluster, nil
}

func (a *ProviderActivities) WaitForReady(ctx context.Context, input WaitForReadyInput) error {
	p, err := a.service(input.Cloud, input.Provider)
	if err != nil {
		return err
	}
	return p.WaitForReady(ctx, input.Cluster)
}

// TODO: expired AWS credentials exhaust the default retries within seconds and
// leave the cluster running. Schedule this activity with a slower retry policy
// (30s backoff up to 5m, bounded by a ScheduleToCloseTimeout).
func (a *ProviderActivities) Deprovision(ctx context.Context, input DeprovisionInput) error {
	p, err := a.service(input.Cloud, input.Provider)
	if err != nil {
		return err
	}
	return p.Deprovision(ctx, input.ClusterID)
}

// Disrupt interrupts the running cluster and returns refreshed connection info.
// It returns once the cluster has settled, so a caller disrupting repeatedly
// does not overlap one recovery with the next.
func (a *ProviderActivities) Disrupt(ctx context.Context, input DisruptInput) (provider.ClusterInfo, error) {
	p, err := a.service(input.Cloud, input.Provider)
	if err != nil {
		return provider.ClusterInfo{}, err
	}
	cluster, err := p.Disrupt(ctx, input.Cluster, input.Disruption)
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("%s cluster: %w", input.Disruption, err)
	}
	return cluster, nil
}

// SwitchoverReadiness reports whether the cluster accepts the next disruption now.
func (a *ProviderActivities) SwitchoverReadiness(ctx context.Context, input SwitchoverReadinessInput) (bool, error) {
	p, err := a.service(input.Cloud, input.Provider)
	if err != nil {
		return false, err
	}
	return p.SwitchoverReadiness(ctx, input.Cluster)
}

// CheckSupported reports whether the service runs in the cloud and supports the
// disruption, from the registries alone, before anything is provisioned.
func (a *ProviderActivities) CheckSupported(ctx context.Context, input CheckSupportedInput) error {
	// Retrying cannot change either answer.
	if !cloud.Supports(input.Cloud, input.Provider) {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("%s does not run in %s", input.Provider, input.Cloud),
			"UnsupportedCloud", nil,
		)
	}
	if !slices.Contains(dbservice.Disruptions(input.Provider, input.Request), input.Disruption) {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("provider %q cannot %s this cluster", input.Provider, input.Disruption),
			"UnsupportedDisruption", nil,
		)
	}
	if a.tel != nil {
		a.tel.Logger.Info("disruption supported",
			slog.String("cloud", string(input.Cloud)),
			slog.String("provider", string(input.Provider)),
			slog.String("disruption", string(input.Disruption)),
		)
	}
	return nil
}

// service builds the named database service in the named cloud.
func (a *ProviderActivities) service(cloudName provider.CloudName, name provider.DBServiceName) (dbservice.DBService, error) {
	c, err := cloud.NewCloudProvider(cloudName)
	if err != nil {
		return nil, fmt.Errorf("cloud %q: %w", cloudName, err)
	}
	service, err := dbservice.NewDBService(name, c, a.tel)
	if err != nil {
		return nil, fmt.Errorf("database service %q: %w", name, err)
	}
	return service, nil
}
