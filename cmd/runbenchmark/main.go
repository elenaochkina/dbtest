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
)

func main() {
	providerName := flag.String("provider",  "docker",        "provider name (docker)")
	scenarioName := flag.String("scenario",  "all",           "scenario to run (warehouse, benchmark, all)")
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

	//State DB
	stateDSN := os.Getenv("STATE_DSN")
	if stateDSN == "" {
		slog.Error("STATE_DSN is required")
		os.Exit(1)
	}
	statePool, err := state.Connect(stateDSN, tel)
	if err != nil {
		slog.Error("state connect failed", "error", err)
		os.Exit(1)
	}
	defer statePool.Close()

	//return a provider for a requested name
	p, err := provider.Run(provider.ProviderName(*providerName), tel)
	if err != nil {
		slog.Error("provider init failed", "error", err)
		os.Exit(1)
	}

	rc := &scenario.RunContext{
		Cfg: scenario.Config{
			Provider:    provider.ProviderName(*providerName),
			Seed:        *seed,
			Warehouses:  *warehouses,
			ScaleFactor: *scaleFactor,
			Clients:     *clients,
			Duration:    *duration,
		},
		Provider:  p,
		StatePool: statePool,
		Tel:       tel,
	}

	if err := scenario.Run(ctx, *scenarioName, rc); err != nil {
		slog.Error("scenario failed", "error", err)
		os.Exit(1)
	}

	fmt.Println("\npress Enter to shut down the metrics server and exit...")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
