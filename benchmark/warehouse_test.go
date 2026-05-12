package benchmark_test

import (
	"context"
	"os"
	"testing"

	"github.com/elenaochkina/dbtest/pgadapter"
	"github.com/elenaochkina/dbtest/benchmark"
	"github.com/elenaochkina/dbtest/pkg/seedgen"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/validator"
)

func TestWarehouseChecksum(t *testing.T) {
	dsn := os.Getenv("DSN")
	if dsn == "" {
		t.Skip("DSN not set — skipping integration test")
	}

	// initialize telemetry
	tel := telemetry.Init(telemetry.Config{
		Log: telemetry.LogConfig{LogLevel: "info"},
		Metrics: telemetry.MetricsConfig{MetricsPort: 9090},
	})
	defer tel.Shutdown()

	ctx := context.Background()

	pool, err := pgadapter.Connect(dsn, pgadapter.WithMetrics(tel))
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

	// Run one order cycle: pick warehouse 1, decrease its stock by 10.
	if err := benchmark.RunOrderCycle(ctx, pool); err != nil {
		t.Fatalf("order cycle: %v", err)
	}

	// Snapshot again after the operation.
	after, err := validator.ComputeChecksum(ctx, pool, "warehouse", tel)
	if err != nil {
		t.Fatalf("checksum after: %v", err)
	}

	// The number of warehouse rows must be unchanged; total stock must be exactly 10 less.
	validator.AssertDelta(t, before, after, -10)
}
