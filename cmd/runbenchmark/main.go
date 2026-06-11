package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/elenaochkina/dbtest/provider"
	_ "github.com/elenaochkina/dbtest/provider/docker"
	"github.com/elenaochkina/dbtest/scenario"
	"github.com/elenaochkina/dbtest/state"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/workload"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	providerName := flag.String("provider",  "docker",        "provider name (docker)")
	workloadName := flag.String("workload",  "all",           "workload to run (warehouse, pgbench, all)")
	seed         := flag.Int64("seed",        42,             "random seed for warehouse data")
	warehouses   := flag.Int("warehouses",     5,             "number of warehouse rows to seed")
	scaleFactor  := flag.Int("scale",          1,             "pgbench scale factor")
	clients      := flag.Int("clients",        4,             "pgbench client count")
	duration     := flag.Duration("duration", 15*time.Second, "pgbench run duration")
	flag.Parse()

	tel := telemetry.Init(telemetry.Config{
		Log:     telemetry.LogConfig{LogLevel: "info", Output: nil},
		Metrics: telemetry.MetricsConfig{MetricsPort: 9090},
	})
	defer tel.Shutdown()

	fmt.Println("metrics server running → http://localhost:9090/metrics")

	ctx := context.Background()

	// State DB is optional — orphan tracking and reconnect skipped if STATE_DSN is not set.
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

	s, err := scenario.New(scenario.Config{
		Provider:    provider.ProviderName(*providerName),
		Workload:    workload.WorkloadName(*workloadName),
		StatePool:   statePool,
		Seed:        *seed,
		Warehouses:  *warehouses,
		ScaleFactor: *scaleFactor,
		Clients:     *clients,
		Duration:    *duration,
	})
	if err != nil {
		slog.Error("scenario.New failed", "error", err)
		os.Exit(1)
	}

	if err := s.Run(ctx, tel); err != nil {
		slog.Error("scenario failed", "error", err)
		os.Exit(1)
	}

	fmt.Println("\npress Enter to shut down the metrics server and exit...")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
