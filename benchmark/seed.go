package benchmark

import (
	"context"
	"fmt"

	"github.com/elenaochkina/dbtest/pkg/seedgen"
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
func SeedWarehouses(ctx context.Context, db *pgxpool.Pool, seeder *seedgen.Seeder, count int) error {
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
	}
	return nil
}

// DropWarehouseTable removes the warehouse table.
// Used at the start of tests to ensure a clean slate.
func DropWarehouseTable(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `DROP TABLE IF EXISTS warehouse`)
	return err
}
