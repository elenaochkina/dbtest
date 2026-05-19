package benchmark_test

import (
	"context"
	"os"
	"testing"

	"github.com/elenaochkina/dbtest/benchmark"
	"github.com/elenaochkina/dbtest/pgadapter"
	"github.com/elenaochkina/dbtest/pkg/seedgen"
	"github.com/elenaochkina/dbtest/state"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/validator"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWarehouseChecksum(t *testing.T) {
	dsn := os.Getenv("DSN")
	if dsn == "" {
		t.Skip("DSN not set — skipping integration test")
	}

	// initialize telemetry
	tel := telemetry.Init(telemetry.Config{
		Log:     telemetry.LogConfig{LogLevel: "info", Output: nil},
		Metrics: telemetry.MetricsConfig{MetricsPort: 9090},
	})
	defer tel.Shutdown()

	ctx := context.Background()

	// State store is optional. If STATE_DSN is not set, all state tracking is
	// skipped and the test behaves exactly as it did in Stage 2.
	var ss *pgxpool.Pool
	if stateDSN := os.Getenv("STATE_DSN"); stateDSN != "" {
		var err error
		ss, err = state.Connect(stateDSN, tel)
		if err != nil {
			t.Fatalf("state connect: %v", err)
		}
		defer ss.Close()
	}

	// Start a run in the state store so this execution is recorded.
	// The deferred End call marks the run as passed or failed once the test returns.
	var run *state.Run
	if ss != nil {
		var err error
		run, err = state.StartRun(ctx, ss, state.RunConfig{
			Seed:     42,
			Scenario: "warehouse-consistency",
			Provider: "manual",
		}, tel)
		if err != nil {
			t.Fatalf("start run: %v", err)
		}
		defer func() {
			if err := run.End(ctx, !t.Failed()); err != nil {
				t.Logf("end run: %v", err)
			}
		}()
	}

	pool, err := pgadapter.Connect(dsn, tel)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	if err := benchmark.DropOrdersTable(ctx, pool); err != nil {
		t.Fatalf("drop orders: %v", err)
	}
	if err := benchmark.DropWarehouseTable(ctx, pool); err != nil {
		t.Fatalf("drop warehouse: %v", err)
	}

	// Create Warehouse and Orders table
	if err := benchmark.CreateWarehouseTable(ctx, pool); err != nil {
		t.Fatalf("create warehouse: %v", err)
	}
	if err := benchmark.CreateOrdersTable(ctx, pool); err != nil {
		t.Fatalf("create orders: %v", err)
	}

	// Seed 5 warehouses with a fixed seed — same seed always produces the same rows.
	seeder := seedgen.New(42)
	if err := benchmark.SeedWarehouses(ctx, pool, seeder, 5, tel); err != nil {
		t.Fatalf("seed warehouses: %v", err)
	}

	// Snapshot the table state before we do anything.
	before, err := validator.ComputeChecksum(ctx, pool, "warehouse", tel)
	if err != nil {
		t.Fatalf("checksum before: %v", err)
	}
	if run != nil {
		if err := run.Checkpoint(ctx, "after_seed", before); err != nil {
			t.Fatalf("checkpoint after_seed: %v", err)
		}
	}

	// Run one order cycle: pick warehouse 1, decrease its stock by 10.
	if err := benchmark.RunOrderCycle(ctx, pool); err != nil {
		t.Fatalf("order cycle: %v", err)
	}

	// Snapshot again after the operation.
	after, err := validator.ComputeChecksum(ctx, pool, "warehouse", tel)
	if err != nil {
		t.Fatalf("checksum after: %v", err)
	}
	if run != nil {
		if err := run.Checkpoint(ctx, "after_orders", after); err != nil {
			t.Fatalf("checkpoint after_orders: %v", err)
		}
	}

	// The number of warehouse rows must be unchanged; total stock must be exactly 10 less.
	validator.AssertDelta(t, before, after, -10)

	// Compare both checkpoints against the previous passing run for this scenario.
	// If no prior run exists (first ever run), LastRun returns nil and we skip.
	// The ID check prevents a run from comparing against itself when the test
	// binary is reused within a session.
	if run != nil {
		prior, err := state.LastRun(ctx, ss, "warehouse-consistency")
		if err != nil {
			t.Fatalf("last run: %v", err)
		}
		if prior != nil && prior.ID != run.ID {
			state.AssertMatchesPrior(t, ctx, run, prior, "after_seed")
			state.AssertMatchesPrior(t, ctx, run, prior, "after_orders")
		}
	}
}
