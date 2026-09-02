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
	Image   string
	Args    []string // appended to the image's entrypoint
	Name    string
	Network string
}

// Handle identifies a started container: a container ID on Docker, a task ARN
// on ECS.
type Handle struct{ ID string }

// Runner starts a container and lets the caller wait for it, stop it, and read
// its streams.
// Output streams returns a container stdout as result

type Runner interface {
	Start(ctx context.Context, spec Spec) (Handle, error)

	// Wait blocks until the container exits and returns its exit code.
	Wait(ctx context.Context, h Handle) (int, error)

	// Stop asks the container to exit and waits for it. The container is left in
	// place so its streams can still be read.
	Stop(ctx context.Context, h Handle) error

	// Remove discards the container and everything it printed. Read Output first.
	Remove(ctx context.Context, h Handle) error

	Output(ctx context.Context, h Handle) ([]byte, error)

	Logs(ctx context.Context, h Handle) ([]byte, error)
}

// RunnerName is the typed identifier for a Runner implementation.
type RunnerName string

const (
	Docker RunnerName = "docker"
	ECS    RunnerName = "ecs"
)

var registry = map[RunnerName]func(*telemetry.Telemetry) (Runner, error){}

func Register(name RunnerName, fn func(*telemetry.Telemetry) (Runner, error)) {
	registry[name] = fn
}

// Run returns a Runner for the given name.
func Run(name RunnerName, tel *telemetry.Telemetry) (Runner, error) {
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
