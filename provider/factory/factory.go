// Package factory is the single place that maps provider names to implementations.
package factory

import (
	"fmt"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/docker"
	"github.com/elenaochkina/dbtest/telemetry"
)

// Run returns a Provider for the given name.
// tel may be nil — metrics and logs are skipped when nil.
// TODO (Stage 8/9): add "aws" case importing provider/aws.
func Run(providerName string, tel *telemetry.Telemetry) (provider.Provider, error) {
	switch providerName {
	case "docker":
		return docker.New(tel)
	default:
		return nil, fmt.Errorf("unknown provider %q", providerName)
	}
}
