package benchmark_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/elenaochkina/dbtest/adapter"
	"github.com/elenaochkina/dbtest/benchmark"
	"github.com/elenaochkina/dbtest/pkg/seedgen"
	"github.com/elenaochkina/dbtest/validator"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWarehouseChecksum(t *testing.T) {
	dsn := os.Getenv("DSN")
	if dsn == "" {
		t.Skip("DSN not set — skipping integration test")
	}

	ctx := context.Background()

	pool, err := adapter.Connect(dsn)
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
	if err := benchmark.SeedWarehouses(ctx, pool, seeder, 5); err != nil {
		t.Fatalf("seed warehouses: %v", err)
	}

	// Snapshot the table state before we do anything.
	before, err := validator.ComputeChecksum(ctx, pool, "warehouse")
	if err != nil {
		t.Fatalf("checksum before: %v", err)
	}

	// Run one order cycle: pick warehouse 1, decrease its stock by 10.
	if err := runOrderCycle(ctx, pool); err != nil {
		t.Fatalf("order cycle: %v", err)
	}

	// Snapshot again after the operation.
	after, err := validator.ComputeChecksum(ctx, pool, "warehouse")
	if err != nil {
		t.Fatalf("checksum after: %v", err)
	}

	// The number of warehouse rows must be unchanged; total stock must be exactly 10 less.
	validator.AssertDelta(t, before, after, -10)
}

// runOrderCycle simulates a minimal order: decrease warehouse 1 stock by 10
// and record it in the orders table.
// Both statements run inside a single transaction — either both commit or both roll back.
func runOrderCycle(ctx context.Context, db *pgxpool.Pool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	// Rollback is a no-op if Commit succeeds, so this is always safe to defer.
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`UPDATE warehouse SET stock_count = stock_count - 10 WHERE id = 1`,
	)
	if err != nil {
		return fmt.Errorf("update warehouse stock: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO orders (warehouse_id, quantity) VALUES (1, 10)`,
	)
	if err != nil {
		return fmt.Errorf("insert order: %w", err)
	}

	return tx.Commit(ctx)
}
