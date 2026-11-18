package main

import (
	"context"
	"fmt"
	"time"

	"github.com/elenaochkina/dbtest/hammerdb"
)

// newHammerDB returns the TPROC-C tool for a schema of warehouses at dsn.
// prepare snapshots the schema and maintain restores it.
func newHammerDB(dsn string, warehouses int) tool {
	var base hammerdb.Baseline
	return tool{
		workload:   "tpcc",
		throughput: "nopm",
		minClients: 8,
		maxClients: 128,
		rampUp:     time.Minute,
		duration:   3 * time.Minute,
		prepare: func(ctx context.Context) error {
			var err error
			if base, err = hammerdb.Snapshot(ctx, dsn); err != nil {
				return fmt.Errorf("snapshot: %w", err)
			}
			return nil
		},
		maintain: func(ctx context.Context) (bool, error) {
			return hammerdb.Maintain(ctx, dsn, base)
		},
		run: func(ctx context.Context, clients int, rampUp, duration time.Duration) (map[string]float64, error) {
			out, err := hammerdb.Run(ctx, dsn, hammerdb.Config{Warehouses: warehouses, Clients: clients, RampUp: rampUp, Duration: duration})
			if err != nil {
				return nil, err
			}
			return out.Metrics(), nil
		},
	}
}
