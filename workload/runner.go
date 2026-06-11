package workload

import (
	"context"
	"fmt"

	"github.com/elenaochkina/dbtest/telemetry"
	"golang.org/x/sync/errgroup"
)

type sequentialRunner struct {
	name      string
	workloads []Workload
}

// Sequential runs workloads one after another, stopping on the first error.
func Sequential(name string, workloads ...Workload) Workload {
	return &sequentialRunner{name: name, workloads: workloads}
}

func (r *sequentialRunner) Name() string { return r.name }

func (r *sequentialRunner) Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) error {
	for _, s := range r.workloads {
		if err := s.Run(ctx, dsn, tel); err != nil {
			return fmt.Errorf("workload %q: %w", s.Name(), err)
		}
	}
	return nil
}

type parallelRunner struct {
	name      string
	workloads []Workload
}

// Parallel runs all workloads concurrently via errgroup, cancelling all on the first error.
// Each workload must manage its own pool — parallel workloads share the DSN
// but must not share a *pgxpool.Pool or write to the same tables.
func Parallel(name string, workloads ...Workload) Workload {
	return &parallelRunner{name: name, workloads: workloads}
}

func (r *parallelRunner) Name() string { return r.name }

func (r *parallelRunner) Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) error {
	eg, ctx := errgroup.WithContext(ctx)
	for _, s := range r.workloads {
		eg.Go(func() error {
			if err := s.Run(ctx, dsn, tel); err != nil {
				return fmt.Errorf("workload %q: %w", s.Name(), err)
			}
			return nil
		})
	}
	return eg.Wait()
}

type repeatRunner struct {
	workload Workload
	times    int
}

// Repeat runs a single workload N times, reporting which repetition failed.
func Repeat(workload Workload, times int) Workload {
	return &repeatRunner{workload: workload, times: times}
}

func (r *repeatRunner) Name() string {
	return fmt.Sprintf("%s(x%d)", r.workload.Name(), r.times)
}

func (r *repeatRunner) Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) error {
	for i := range r.times {
		if err := r.workload.Run(ctx, dsn, tel); err != nil {
			return fmt.Errorf("repetition %d/%d: %w", i+1, r.times, err)
		}
	}
	return nil
}
