// Package cloud holds the clouds that run database services and harness containers.
package cloud

import (
	"context"
	"fmt"
	"maps"
	"slices"

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

// New builds the named cloud.
func New(name provider.CloudName) (Cloud, error) {
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
