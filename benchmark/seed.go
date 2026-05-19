package benchmark

import (
	"context"
	"fmt"

	"github.com/elenaochkina/dbtest/pkg/seedgen"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateWarehouseTable creates the warehouse table if it does not already exist.
func CreateWarehouseTable(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS warehouse (
			id          SERIAL PRIMARY KEY,
			name        TEXT NOT NULL,
			stock_count INT  NOT NULL
		)
	`)
	return err
}

// SeedWarehouses inserts count rows into the warehouse table.
// The seeder controls which stock values are generated — same seed = same rows every run.
func SeedWarehouses(ctx context.Context, db *pgxpool.Pool, seeder *seedgen.Seeder, count int, tel *telemetry.Telemetry) error {
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("Warehouse-%d", i)
		stock := seeder.StockCount()
		_, err := db.Exec(ctx,
			`INSERT INTO warehouse (name, stock_count) VALUES ($1, $2)`,
			name, stock,
		)
		if err != nil {
			return fmt.Errorf("seed warehouse %d: %w", i, err)
		}
		if tel != nil {
			tel.Metrics.SeedRowsTotal.WithLabelValues("warehouse").Inc()
			tel.Logger.With("package", "benchmark").Info("seeded row", "table", "warehouse", "id", i)
		}
	}
	return nil
}

// DropWarehouseTable removes the warehouse table.
// Used at the start of tests to ensure a clean slate.
func DropWarehouseTable(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `DROP TABLE IF EXISTS warehouse`)
	return err
}

// CreateOrdersTable creates the orders table if it does not already exist.
func CreateOrdersTable(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS orders (
			id           SERIAL PRIMARY KEY,
			warehouse_id INT NOT NULL,
			quantity     INT NOT NULL
		)
	`)
	return err
}

// DropOrdersTable removes the orders table.
// Used at the start of tests to ensure a clean slate.
func DropOrdersTable(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `DROP TABLE IF EXISTS orders`)
	return err
}

// RunOrderCycle decreases warehouse 1 stock by 10 and records the order.
// Both statements run inside a single transaction — either both commit or both roll back.
func RunOrderCycle(ctx context.Context, db *pgxpool.Pool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `UPDATE warehouse SET stock_count = stock_count - 10 WHERE id = 1`)
	if err != nil {
		return fmt.Errorf("update warehouse stock: %w", err)
	}

	_, err = tx.Exec(ctx, `INSERT INTO orders (warehouse_id, quantity) VALUES (1, 10)`)
	if err != nil {
		return fmt.Errorf("insert order: %w", err)
	}

	return tx.Commit(ctx)
}
