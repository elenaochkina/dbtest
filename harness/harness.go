// Package harness launches short-lived containers and reads back what they
// printed.
package harness

import (
	"context"
	"fmt"
	"sort"

	"github.com/elenaochkina/dbtest/telemetry"
)

type Spec struct {
	Image string
	Args  []string // appended to the image's entrypoint
	Name  string
	// SelfExits marks a container that ends on its own, the way bench does when
	// its run finishes. Stop waits for those rather than terminating them.
	SelfExits bool
}

// Handle is the runner's identifier for a started container. It carries
// everything Stop needs: Run and Stop happen in separate activities, and nothing
// else travels between them.
type Handle struct {
	ID        string
	SelfExits bool
}

// Runner starts a container somewhere and collects what it printed.
type Runner interface {
	// Run creates and starts the container and returns once it is running.
	Run(ctx context.Context, spec Spec) (Handle, error)

	// Stop ends the container and returns everything it printed, stdout and
	// stderr together. Fargate cannot separate the two, so nothing may depend on
	// the split. Output is returned even when the error is non-nil, because a
	// container that exits non-zero has usually still printed its result.
	//
	// Calling it twice is not an error, but only the first call returns output:
	// the second finds the container already gone and discard an error.
	Stop(ctx context.Context, h Handle) ([]byte, error)
}

// RunnerName is the typed identifier for a Runner implementation.
type RunnerName string

const (
	Docker  RunnerName = "docker"
	Fargate RunnerName = "fargate"
)

var registry = map[RunnerName]func(*telemetry.Telemetry) (Runner, error){}

func Register(name RunnerName, fn func(*telemetry.Telemetry) (Runner, error)) {
	registry[name] = fn
}

// New returns a Runner for the given name.
func New(name RunnerName, tel *telemetry.Telemetry) (Runner, error) {
	fn, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown runner %q; registered: %v", name, registeredNames())
	}
	return fn(tel)
}

func registeredNames() []string {
	names := make([]string, 0, len(registry))
	for k := range registry {
		names = append(names, string(k))
	}
	sort.Strings(names)
	return names
}
