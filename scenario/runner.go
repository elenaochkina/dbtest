package scenario

import (
	"context"
	"fmt"

	"github.com/elenaochkina/dbtest/telemetry"
	"golang.org/x/sync/errgroup"
)

type sequentialRunner struct {
	name      string
	scenarios []Scenario
}

// Sequential runs scenarios one after another, stopping on the first error.
func Sequential(name string, scenarios ...Scenario) Scenario {
	return &sequentialRunner{name: name, scenarios: scenarios}
}

func (r *sequentialRunner) Name() string { return r.name }

func (r *sequentialRunner) Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) error {
	for _, s := range r.scenarios {
		if err := s.Run(ctx, dsn, tel); err != nil {
			return fmt.Errorf("scenario %q: %w", s.Name(), err)
		}
	}
	return nil
}

type parallelRunner struct {
	name      string
	scenarios []Scenario
}

// Parallel runs all scenarios concurrently via errgroup, cancelling all on the first error.
// Each scenario must manage its own pool — parallel scenarios share the DSN
// but must not share a *pgxpool.Pool or write to the same tables.
func Parallel(name string, scenarios ...Scenario) Scenario {
	return &parallelRunner{name: name, scenarios: scenarios}
}

func (r *parallelRunner) Name() string { return r.name }

func (r *parallelRunner) Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) error {
	eg, ctx := errgroup.WithContext(ctx)
	for _, s := range r.scenarios {
		eg.Go(func() error {
			if err := s.Run(ctx, dsn, tel); err != nil {
				return fmt.Errorf("scenario %q: %w", s.Name(), err)
			}
			return nil
		})
	}
	return eg.Wait()
}

type repeatRunner struct {
	scenario Scenario
	times    int
}

// Repeat runs a single scenario N times, reporting which repetition failed.
func Repeat(scenario Scenario, times int) Scenario {
	return &repeatRunner{scenario: scenario, times: times}
}

func (r *repeatRunner) Name() string {
	return fmt.Sprintf("%s(x%d)", r.scenario.Name(), r.times)
}

func (r *repeatRunner) Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) error {
	for i := range r.times {
		if err := r.scenario.Run(ctx, dsn, tel); err != nil {
			return fmt.Errorf("repetition %d/%d: %w", i+1, r.times, err)
		}
	}
	return nil
}
