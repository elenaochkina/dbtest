package scenario

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/elenaochkina/dbtest/telemetry"
)

// Scenario is the interface every scenario must satisfy.
type Scenario interface {
	Name() string
	Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) error
}

// ScenarioName is the typed identifier for a scenario.
type ScenarioName string

const (
	Warehouse ScenarioName = "warehouse"
	Pgbench   ScenarioName = "pgbench"
	All       ScenarioName = "all"
)

// Config holds all parameters for any scenario.
// Each scenario reads only the fields it needs.
type Config struct {
	// warehouse
	Seed       int64
	Warehouses int
	// pgbench
	ScaleFactor  int
	Clients      int
	Duration     time.Duration
	ProviderName string
}

// registry maps scenario names to constructor functions.
// Populated by each scenario file via init() + Register().
var registry = map[ScenarioName]func(Config) Scenario{}

// Register adds a scenario constructor to the registry.
// Call this from init() in each scenario file.
func Register(name ScenarioName, fn func(Config) Scenario) {
	registry[name] = fn
}

// New returns a Scenario for the given name.
// All is a special case that composes Warehouse and Pgbench sequentially —
// it is not in the registry since its definition depends on other entries.
func New(name ScenarioName, cfg Config) (Scenario, error) {
	if name == All {
		w, err := New(Warehouse, cfg)
		if err != nil {
			return nil, err
		}
		p, err := New(Pgbench, cfg)
		if err != nil {
			return nil, err
		}
		return Sequential("all", w, p), nil
	}
	fn, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown scenario %q; registered: %v", name, registeredNames())
	}
	return fn(cfg), nil
}

func registeredNames() []string {
	names := make([]string, 0, len(registry))
	for k := range registry {
		names = append(names, string(k))
	}
	sort.Strings(names)
	return names
}
