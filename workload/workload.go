package workload

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/elenaochkina/dbtest/telemetry"
)

// Workload is the interface every workload must satisfy.
type Workload interface {
	Name() string
	Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) error
}

// WorkloadName is the typed identifier for a workload.
type WorkloadName string

const (
	Warehouse WorkloadName = "warehouse"
	Pgbench   WorkloadName = "pgbench"
	All       WorkloadName = "all"
)

// Config holds all parameters for any workload.
// Each workload reads only the fields it needs.
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

// registry maps workload names to constructor functions.
// Populated by each workload file via init() + Register().
var registry = map[WorkloadName]func(Config) Workload{}

// Register adds a workload constructor to the registry.
// Call this from init() in each workload file.
func Register(name WorkloadName, fn func(Config) Workload) {
	registry[name] = fn
}

// New returns a Workload for the given name.
// All is a special case that composes Warehouse and Pgbench sequentially —
// it is not in the registry since its definition depends on other entries.
func New(name WorkloadName, cfg Config) (Workload, error) {
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
		return nil, fmt.Errorf("unknown workload %q; registered: %v", name, registeredNames())
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
