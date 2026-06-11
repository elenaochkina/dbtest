
package workload

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/elenaochkina/dbtest/benchmark"
	"github.com/elenaochkina/dbtest/pgadapter"
	"github.com/elenaochkina/dbtest/pkg/seedgen"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/validator"
)

func init() {
	Register(Warehouse, func(cfg Config) Workload {
		return &warehouseWorkload{cfg: cfg}
	})
}

type warehouseWorkload struct{ cfg Config }

func (s *warehouseWorkload) Name() string { return string(Warehouse) }

func (s *warehouseWorkload) Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) error {
	pool, err := pgadapter.Connect(dsn, tel)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	if err := benchmark.DropOrdersTable(ctx, pool); err != nil {
		return fmt.Errorf("drop orders: %w", err)
	}
	if err := benchmark.DropWarehouseTable(ctx, pool); err != nil {
		return fmt.Errorf("drop warehouse: %w", err)
	}
	if err := benchmark.CreateWarehouseTable(ctx, pool); err != nil {
		return fmt.Errorf("create warehouse: %w", err)
	}
	if err := benchmark.CreateOrdersTable(ctx, pool); err != nil {
		return fmt.Errorf("create orders: %w", err)
	}

	seeder := seedgen.New(s.cfg.Seed)
	if err := benchmark.SeedWarehouses(ctx, pool, seeder, s.cfg.Warehouses, tel); err != nil {
		return fmt.Errorf("seed warehouses: %w", err)
	}

	before, err := validator.ComputeChecksum(ctx, pool, "warehouse", tel)
	if err != nil {
		return fmt.Errorf("checksum before: %w", err)
	}

	if err := benchmark.RunOrderCycle(ctx, pool); err != nil {
		return fmt.Errorf("order cycle: %w", err)
	}

	after, err := validator.ComputeChecksum(ctx, pool, "warehouse", tel)
	if err != nil {
		return fmt.Errorf("checksum after: %w", err)
	}

	delta := after.StockSum - before.StockSum
	if tel != nil {
		tel.Logger.Info("warehouse workload complete",
			slog.Int64("before_stock", before.StockSum),
			slog.Int64("after_stock", after.StockSum),
			slog.Int64("delta", delta),
		)
	}

	if delta != -10 {
		return fmt.Errorf("expected stock delta -10, got %d", delta)
	}
	return nil
}
