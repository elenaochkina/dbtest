// Package activities holds the side-effecting step logic the workflows schedule.
package activities

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"go.temporal.io/sdk/temporal"
)

type ProvisionInput struct {
	Provider provider.ProviderName
	Request  provider.ProvisionRequest
	Token    string
	Password string
}

type WaitForReadyInput struct {
	Provider provider.ProviderName
	Cluster  provider.ClusterInfo
}

type DeprovisionInput struct {
	Provider  provider.ProviderName
	ClusterID string
}

type DisruptInput struct {
	Provider   provider.ProviderName
	Cluster    provider.ClusterInfo
	Disruption provider.Disruption
}

type CheckSupportedInput struct {
	Provider   provider.ProviderName
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
	p, err := provider.Run(input.Provider, a.tel)
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("provider %q: %w", input.Provider, err)
	}
	cluster, err := p.Provision(ctx, input.Request, input.Token, input.Password)
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("provision: %w", err)
	}
	return cluster, nil
}

func (a *ProviderActivities) WaitForReady(ctx context.Context, input WaitForReadyInput) error {
	p, err := provider.Run(input.Provider, a.tel)
	if err != nil {
		return fmt.Errorf("provider %q: %w", input.Provider, err)
	}
	return p.WaitForReady(ctx, input.Cluster)
}

func (a *ProviderActivities) Deprovision(ctx context.Context, input DeprovisionInput) error {
	p, err := provider.Run(input.Provider, a.tel)
	if err != nil {
		return fmt.Errorf("provider %q: %w", input.Provider, err)
	}
	return p.Deprovision(ctx, input.ClusterID)
}

// Disrupt interrupts the running cluster and returns refreshed connection info.
// It returns once the cluster has settled, so a caller disrupting repeatedly
// does not overlap one recovery with the next.
func (a *ProviderActivities) Disrupt(ctx context.Context, input DisruptInput) (provider.ClusterInfo, error) {
	p, err := provider.Run(input.Provider, a.tel)
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("provider %q: %w", input.Provider, err)
	}
	cluster, err := p.Disrupt(ctx, input.Cluster, input.Disruption)
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("%s cluster: %w", input.Disruption, err)
	}
	return cluster, nil
}

// Needs to check whether a certain disruption is supported by a requested provider before faces an eror or waste budget.
func (a *ProviderActivities) CheckSupported(ctx context.Context, input CheckSupportedInput) error {
	p, err := provider.Run(input.Provider, a.tel)
	if err != nil {
		return fmt.Errorf("provider %q: %w", input.Provider, err)
	}
	if !p.Supports(input.Request, input.Disruption) {
		// Retrying cannot change the answer.
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("provider %q cannot %s this cluster", input.Provider, input.Disruption),
			"UnsupportedDisruption", nil,
		)
	}
	if a.tel != nil {
		a.tel.Logger.Info("disruption supported",
			slog.String("provider", string(input.Provider)),
			slog.String("disruption", string(input.Disruption)),
		)
	}
	return nil
}
