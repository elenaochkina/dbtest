package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/elenaochkina/dbtest/pgadapter"
	"github.com/elenaochkina/dbtest/benchmark"
	"github.com/elenaochkina/dbtest/pkg/seedgen"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/validator"
)

func main() {
	dsn := os.Getenv("DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DSN env var is required")
		os.Exit(1)
	}

	tel := telemetry.Init(telemetry.Config{
		Log:     telemetry.LogConfig{LogLevel: "info", Output: nil},
		Metrics: telemetry.MetricsConfig{MetricsPort: 9090},
	})
	defer tel.Shutdown()

	fmt.Println("metrics server running → http://localhost:9090/metrics")

	ctx := context.Background()

	pool, err := pgadapter.Connect(dsn, tel)
	if err != nil {
		slog.Error("connect failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// clean slate
	if err := benchmark.DropOrdersTable(ctx, pool); err != nil {
		slog.Error("drop orders", "error", err)
		os.Exit(1)
	}
	if err := benchmark.DropWarehouseTable(ctx, pool); err != nil {
		slog.Error("drop warehouse", "error", err)
		os.Exit(1)
	}
	if err := benchmark.CreateWarehouseTable(ctx, pool); err != nil {
		slog.Error("create warehouse", "error", err)
		os.Exit(1)
	}
	if err := benchmark.CreateOrdersTable(ctx, pool); err != nil {
		slog.Error("create orders", "error", err)
		os.Exit(1)
	}

	// seed with fixed seed so output is always the same
	seeder := seedgen.New(42)
	if err := benchmark.SeedWarehouses(ctx, pool, seeder, 5, tel); err != nil {
		slog.Error("seed warehouses", "error", err)
		os.Exit(1)
	}

	before, err := validator.ComputeChecksum(ctx, pool, "warehouse", tel)
	if err != nil {
		slog.Error("checksum before", "error", err)
		os.Exit(1)
	}
	fmt.Printf("before: rows=%d stock_sum=%d\n", before.RowCount, before.StockSum)

	if err := benchmark.RunOrderCycle(ctx, pool); err != nil {
		slog.Error("order cycle", "error", err)
		os.Exit(1)
	}

	after, err := validator.ComputeChecksum(ctx, pool, "warehouse", tel)
	if err != nil {
		slog.Error("checksum after", "error", err)
		os.Exit(1)
	}
	fmt.Printf("after:  rows=%d stock_sum=%d  (delta=%d)\n", after.RowCount, after.StockSum, after.StockSum-before.StockSum)

	fmt.Println("\npress Enter to shut down the metrics server and exit...")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
