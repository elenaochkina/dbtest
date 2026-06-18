// Package scenario is the top layer: it sequences control-plane and data-plane
// primitives into an ordered script of steps over a shared run context. A
// scenario is picked by name; the runner executes its steps in order and always
// drains the teardown stack on exit. See docs/scenario.md for the full design.
package scenario

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/state"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/workload"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds the parameters every step may read. Each step uses only the
// fields it needs; it is threaded into the RunContext, not consumed directly.
type Config struct {
	Provider provider.ProviderName
	// warehouse
	Seed       int64
	Warehouses int
	// pgbench
	ScaleFactor int
	Clients     int
	Duration    time.Duration
}

// RunContext is the shared bag threaded into every step. Earlier steps write
// fields that later steps read: provision writes Cluster, which the workload
// steps consume via the DSN. The caller resolves Provider before calling Run.
type RunContext struct {
	Cfg       Config
	Provider  provider.Provider
	Cluster   provider.ClusterInfo
	StatePool *pgxpool.Pool // optional — state-backed steps skipped when nil
	Tel       *telemetry.Telemetry

	// StateRun is the runs-table record for this execution, opened by Run when
	// StatePool is set and closed in teardown. Steps attach their results to it
	// via its run_id. Nil when no state DB is configured.
	StateRun *state.Run

	// Result is the last workload's result, set by workloadStep and consumed by
	// the (future) save-result step. Later workloads overwrite earlier ones.
	Result workload.Result

	cleanups []func(context.Context) // teardown stack — drained in reverse by Run
}

// onCleanup pushes an undo onto the teardown stack. Closures run in reverse
// registration order when Run exits, regardless of how the scenario ended.
func (rc *RunContext) onCleanup(undo func(context.Context)) {
	rc.cleanups = append(rc.cleanups, undo)
}

// Step is one action in a scenario. It reads and writes the shared RunContext.
type Step interface {
	Name() string
	Run(ctx context.Context, rc *RunContext) error
}

// Scenario is an ordered list of steps registered under a name.
type Scenario struct {
	name  string
	steps []Step
}

var registry = map[string]Scenario{}

// Register adds a named scenario. Called from init(); adding a scenario is a
// one-line registration and the engine never changes.
func Register(name string, steps ...Step) {
	registry[name] = Scenario{name: name, steps: steps}
}

// Names returns the registered scenario names, sorted — for help and errors.
func Names() []string {
	names := make([]string, 0, len(registry))
	for k := range registry {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// executeSteps runs the steps in order, logging each, stopping on the first error.
func (s Scenario) executeSteps(ctx context.Context, rc *RunContext) error {
	for _, step := range s.steps {
		if rc.Tel != nil {
			rc.Tel.Logger.Info("step start", "scenario", s.name, "step", step.Name())
		}
		if err := step.Run(ctx, rc); err != nil {
			return fmt.Errorf("step %q: %w", step.Name(), err)
		}
	}
	return nil
}

// Run looks up the named scenario and executes it, guaranteeing teardown. The
// cleanup stack is drained in reverse on every exit path — success, a failed
// step, or a panic-unwind. It does not survive a process crash.
func Run(ctx context.Context, name string, rc *RunContext) (err error) {
	sc, ok := registry[name]
	if !ok {
		return fmt.Errorf("unknown scenario %q; registered: %v", name, Names())
	}

	// Open the runs-table record for this execution. Steps attach results to it
	if rc.StatePool != nil && rc.Tel != nil {
		run, startErr := state.StartRun(ctx, rc.StatePool, state.RunConfig{
			Seed:     rc.Cfg.Seed,
			Scenario: name,
			Provider: string(rc.Cfg.Provider),
		}, rc.Tel)
		if startErr != nil {
			return fmt.Errorf("start run: %w", startErr)
		}
		rc.StateRun = run
	}

	defer func() {
		for i := len(rc.cleanups) - 1; i >= 0; i-- { // reverse order
			rc.cleanups[i](context.Background())
		}
		if rc.StateRun != nil {
			if endErr := rc.StateRun.End(context.Background(), err == nil); endErr != nil && rc.Tel != nil {
				rc.Tel.Logger.Error("end run failed", "error", endErr)
			}
		}
	}()
	return sc.executeSteps(ctx, rc)
}

func toWorkloadConfig(cfg Config) workload.Config {
	return workload.Config{
		Seed:         cfg.Seed,
		Warehouses:   cfg.Warehouses,
		ScaleFactor:  cfg.ScaleFactor,
		Clients:      cfg.Clients,
		Duration:     cfg.Duration,
		ProviderName: string(cfg.Provider),
	}
}
