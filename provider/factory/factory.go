// Package factory is the single place that maps provider names to implementations.
package factory

import (
	"fmt"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/docker"
	"github.com/elenaochkina/dbtest/telemetry"
)

// ProviderName is the typed identifier for a provider implementation.
type ProviderName string

const (
	Docker ProviderName = "docker"
	AWS    ProviderName = "aws" // TODO (Stage 8/9): implement
)

// Run returns a Provider for the given name.
// tel may be nil — metrics and logs are skipped when nil.
func Run(providerName ProviderName, tel *telemetry.Telemetry) (provider.Provider, error) {
	switch providerName {
	case Docker:
		return docker.New(tel)
	default:
		return nil, fmt.Errorf("unknown provider %q", providerName)
	}
}
