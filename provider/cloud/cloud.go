// Package cloud holds the clouds that run database services and harness containers.
package cloud

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
)

// Cloud is where database services and harness containers run.
type Cloud interface {
	Name() provider.CloudName

	// Check reports whether the cloud is configured and logged in.
	Check(ctx context.Context) provider.Status

	// Runners lists the harness runners that start containers in this cloud,
	// the default first.
	Runners() []harness.RunnerName

	// NewRunner builds the named runner.
	NewRunner(name harness.RunnerName, tel *telemetry.Telemetry) (harness.Runner, error)

	// Images returns what the named runner starts for the probe and bench.
	Images(name harness.RunnerName) harness.Images

	// Supports reports whether service runs in this cloud.
	Supports(service provider.DBServiceName) bool
}

var (
	registeredClouds   = map[provider.CloudName]func() (Cloud, error){}
	registeredServices = map[provider.CloudName][]provider.DBServiceName{}
)

// Register adds a cloud's constructor.
func Register(name provider.CloudName, fn func() (Cloud, error)) {
	registeredClouds[name] = fn
}

// NewCloudProvider builds the named cloud.
func NewCloudProvider(name provider.CloudName) (Cloud, error) {
	fn, ok := registeredClouds[name]
	if !ok {
		return nil, fmt.Errorf("unknown cloud %q; registered: %v", name, Names())
	}
	return fn()
}

func Names() []provider.CloudName {
	return slices.Sorted(maps.Keys(registeredClouds))
}

// RegisterService records that service runs in the named cloud. dbservice.Register calls it.
func RegisterService(name provider.CloudName, service provider.DBServiceName) {
	registeredServices[name] = append(registeredServices[name], service)
}

// Supports reports whether service runs in the named cloud.
func Supports(name provider.CloudName, service provider.DBServiceName) bool {
	return slices.Contains(registeredServices[name], service)
}

// MissingVariables returns a misconfigured status naming the .env variables
// that are not set, and whether any are missing.
func MissingVariables(names ...string) (provider.Status, bool) {
	var missing []string
	for _, name := range names {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return provider.Status{}, false
	}
	count := fmt.Sprintf("%d variables", len(missing))
	if len(missing) == 1 {
		count = "1 variable"
	}
	return provider.Status{State: provider.StateMisconfigured, Detail: count + " missing: run source .env (" + strings.Join(missing, ", ") + ")"}, true
}

// EnvOr returns the environment variable key, or fallback when it is unset.
func EnvOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// UnknownRunner is the error a cloud returns for a runner it does not build.
func UnknownRunner(c Cloud, name harness.RunnerName) error {
	return fmt.Errorf("%s has no %q runner; it has %v", c.Name(), name, c.Runners())
}
