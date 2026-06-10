package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/elenaochkina/dbtest/benchmark"
	"github.com/elenaochkina/dbtest/pgadapter"
	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/pkg/seedgen"
	"github.com/elenaochkina/dbtest/provider"
	_ "github.com/elenaochkina/dbtest/provider/docker"
	"github.com/elenaochkina/dbtest/state"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/validator"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	providerName := flag.String("provider", "docker", "provider name (docker)")
	flag.Parse()

	tel := telemetry.Init(telemetry.Config{
		Log:     telemetry.LogConfig{LogLevel: "info", Output: nil},
		Metrics: telemetry.MetricsConfig{MetricsPort: 9090},
	})
	defer tel.Shutdown()

	fmt.Println("metrics server running → http://localhost:9090/metrics")

	ctx := context.Background()

	// State DB is optional — orphan tracking is skipped if STATE_DSN is not set.
	var statePool *pgxpool.Pool
	if stateDSN := os.Getenv("STATE_DSN"); stateDSN != "" {
		var err error
		statePool, err = state.Connect(stateDSN, tel)
		if err != nil {
			slog.Error("state connect failed", "error", err)
			os.Exit(1)
		}
		defer statePool.Close()
	}

	p, err := provider.Run(provider.ProviderName(*providerName), tel)
	if err != nil {
		slog.Error("factory.Run failed", "error", err)
		os.Exit(1)
	}

	cluster, err := p.Provision(ctx)
	if err != nil {
		slog.Error("provision failed", "error", err)
		os.Exit(1)
	}

	// Record cluster immediately so a future cleanup job can find it if the process crashes.
	if statePool != nil {
		if err := state.RecordCluster(ctx, statePool, cluster, *providerName, tel); err != nil {
			slog.Error("record cluster failed", "error", err)
		}
	}

	// Single defer keeps Deprovision and MarkDeprovisioned in guaranteed order.
	defer func() {
		depCtx := context.Background()
		if err := p.Deprovision(depCtx, cluster.ID); err != nil {
			slog.Error("deprovision failed", "error", err)
		}
		if statePool != nil {
			if err := state.MarkDeprovisioned(depCtx, statePool, cluster.ID, tel); err != nil {
				slog.Error("mark deprovisioned failed", "error", err)
			}
		}
	}()

	if err := p.WaitForReady(ctx, cluster); err != nil {
		slog.Error("wait for ready failed", "error", err)
		os.Exit(1)
	}

	pool, err := pgadapter.Connect(cluster.DSN, tel)
	if err != nil {
		slog.Error("connect failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

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

	fmt.Println("\n--- pgbench ---")
	result, err := pgbench.RunLocal(ctx, cluster.DSN, pgbench.Config{
		ScaleFactor: 1,
		Clients:     4,
		Duration:    15 * time.Second,
		Provider:    *providerName,
	}, tel)
	if err != nil {
		slog.Error("pgbench failed", "error", err)
		os.Exit(1)
	}
	fmt.Printf("tps=%.1f  latency_avg=%.2f ms  latency_stddev=%.2f ms\n",
		result.TPS, result.LatencyAvgMs, result.LatencyStddevMs)

	fmt.Println("\npress Enter to shut down the metrics server and exit...")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
