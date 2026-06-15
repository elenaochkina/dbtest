package workload

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/elenaochkina/dbtest/telemetry"
)

// Result is the outcome of a workload run. It is domain-neutral: each workload
// returns its own concrete result type (e.g. pgbench.Result, validator.Checksum)
// which satisfies this interface structurally — the primitive packages do not
// import workload. The Metrics map is the observability view (telemetry, logs,
// dashboards); typed persistence still uses the concrete types. Workloads with
// no numeric output may return nil.
type Result interface {
	Metrics() map[string]float64
}

// Workload is the interface every workload must satisfy.
type Workload interface {
	Name() string
	Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) (Result, error)
}

// WorkloadName is the typed identifier for a workload.
type WorkloadName string

const (
	Warehouse WorkloadName = "warehouse"
	Pgbench   WorkloadName = "pgbench"
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

// New returns a Workload for the given name. Composing several workloads is the
// scenario layer's job (an ordered list of steps), so workload itself only ever
// resolves a single workload by name.
func New(name WorkloadName, cfg Config) (Workload, error) {
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
