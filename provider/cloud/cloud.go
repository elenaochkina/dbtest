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
)

// Cloud is where database services and harness containers run.
type Cloud interface {
	Name() provider.CloudName

	// Check reports whether the cloud is configured and logged in.
	Check(ctx context.Context) provider.Status

	// Runner names the harness runner that starts containers in this cloud.
	Runner() harness.RunnerName

	// Supports reports whether service runs in this cloud.
	Supports(service provider.DBServiceName) bool
}

var (
	registry = map[provider.CloudName]func() (Cloud, error){}
	hosted   = map[provider.CloudName][]provider.DBServiceName{}
)

// Register adds a cloud's constructor.
func Register(name provider.CloudName, fn func() (Cloud, error)) {
	registry[name] = fn
}

// NewCloudProvider builds the named cloud.
func NewCloudProvider(name provider.CloudName) (Cloud, error) {
	fn, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown cloud %q; registered: %v", name, Names())
	}
	return fn()
}

func Names() []provider.CloudName {
	return slices.Sorted(maps.Keys(registry))
}

// Host records that service runs in the named cloud. dbservice.Register calls it.
func Host(name provider.CloudName, service provider.DBServiceName) {
	hosted[name] = append(hosted[name], service)
}

// Supports reports whether service runs in the named cloud.
func Supports(name provider.CloudName, service provider.DBServiceName) bool {
	return slices.Contains(hosted[name], service)
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
