package main

import (
	"log/slog"
	"os"

	"github.com/elenaochkina/dbtest/state"
	"github.com/elenaochkina/dbtest/telemetry"
	dbtemporal "github.com/elenaochkina/dbtest/temporal"

	// Register the providers so provider.Run can resolve them inside activities.
	_ "github.com/elenaochkina/dbtest/provider/aws"
	_ "github.com/elenaochkina/dbtest/provider/docker"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// The worker polls the task queue and executes the workflow + activities. It
// holds the live dependencies (state pool, telemetry) injected into the activity
// structs the workflow's typed-nil receivers resolve to at run time.
func main() {
	tel := telemetry.Init(telemetry.Config{
		Log:     telemetry.LogConfig{LogLevel: "info", Output: nil},
		Metrics: telemetry.MetricsConfig{MetricsPort: 9090},
	})
	defer tel.Shutdown()

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

	hostPort := os.Getenv("TEMPORAL_ADDRESS")
	if hostPort == "" {
		hostPort = client.DefaultHostPort // localhost:7233
	}
	c, err := client.Dial(client.Options{HostPort: hostPort})
	if err != nil {
		slog.Error("temporal dial failed", "error", err)
		os.Exit(1)
	}
	defer c.Close()

	w := worker.New(c, dbtemporal.TaskQueue, worker.Options{})
	w.RegisterWorkflow(dbtemporal.PgBenchWorkflow)
	w.RegisterActivity(dbtemporal.NewSaveResultActivities(statePool, tel))
	w.RegisterActivity(dbtemporal.NewProviderActivities(tel))
	w.RegisterActivity(dbtemporal.NewWorkloadActivities(tel))

	slog.Info("worker started", "task_queue", dbtemporal.TaskQueue, "temporal", hostPort)
	if err := w.Run(worker.InterruptCh()); err != nil {
		slog.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}
