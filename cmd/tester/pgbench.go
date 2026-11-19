package main

import (
	"context"
	"fmt"
	"time"

	"github.com/elenaochkina/dbtest/pgbench"
)

// newPgbench returns the tool for pgbench's built-in TPC-B-like script on
// tables already built at dsn. prepare reads their scale factor.
func newPgbench(dsn string) tool {
	var scale int
	return tool{
		workload:   "tpcb-like",
		throughput: "tps",
		minClients: 1,
		maxClients: 256,
		rampUp:     30 * time.Second,
		duration:   2 * time.Minute,
		prepare: func(ctx context.Context) error {
			var err error
			if scale, err = pgbench.Scale(ctx, dsn); err != nil {
				return fmt.Errorf("scale: %w", err)
			}
			return nil
		},
		maintain: func(ctx context.Context) (bool, error) {
			return pgbench.Maintain(ctx, dsn)
		},
		run: func(ctx context.Context, clients int, rampUp, duration time.Duration) (map[string]float64, error) {
			out, err := pgbench.Run(ctx, dsn, pgbench.Config{ScaleFactor: scale, Clients: clients, WarmUp: rampUp, Duration: duration}, nil)
			if err != nil {
				return nil, err
			}
			metrics := out.Metrics()
			metrics["scale"] = float64(scale)
			return metrics, nil
		},
	}
}
