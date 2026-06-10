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
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	providerName  := flag.String("provider",  "docker", "provider name (docker)")
	scenarioName  := flag.String("scenario",  "all",    "scenario to run (warehouse, pgbench, all)")
	seed          := flag.Int64("seed",         42,     "random seed for warehouse data")
	warehouses    := flag.Int("warehouses",      5,     "number of warehouse rows to seed")
	scaleFactor   := flag.Int("scale",           1,     "pgbench scale factor")
	clients       := flag.Int("clients",         4,     "pgbench client count")
	duration      := flag.Duration("duration", 15*time.Second, "pgbench run duration")
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
		slog.Error("provider.Run failed", "error", err)
		os.Exit(1)
	}

	cluster, err := p.Provision(ctx)
	if err != nil {
		slog.Error("provision failed", "error", err)
		os.Exit(1)
	}

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

	s, err := scenario.New(scenario.ScenarioName(*scenarioName), scenario.Config{
		Seed:         *seed,
		Warehouses:   *warehouses,
		ScaleFactor:  *scaleFactor,
		Clients:      *clients,
		Duration:     *duration,
		ProviderName: *providerName,
	})
	if err != nil {
		slog.Error("scenario.New failed", "error", err)
		os.Exit(1)
	}

	if err := s.Run(ctx, cluster.DSN, tel); err != nil {
		slog.Error("scenario failed", "scenario", s.Name(), "error", err)
		os.Exit(1)
	}

	fmt.Println("\npress Enter to shut down the metrics server and exit...")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
