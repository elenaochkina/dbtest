package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/elenaochkina/dbtest/provider"
	dbtemporal "github.com/elenaochkina/dbtest/temporal"
	"github.com/elenaochkina/dbtest/temporal/workflows"
	"github.com/elenaochkina/dbtest/workload"

	"go.temporal.io/sdk/client"
)

// starter triggers one workflow execution and waits for it. It talks only to the Temporal server;
func main() {
	// Parse flags.
	providerName := flag.String("provider", "docker", "provider name (docker, aws)")
	vcpu := flag.Float64("vcpu", 2, "cluster vCPU")
	memoryMiB := flag.Int("memory-mib", 2048, "cluster memory (MiB)")
	diskGiB := flag.Int("disk-gib", 0, "cluster disk (GiB); 0 = provider default")
	pgVersion := flag.String("pg-version", "16", "postgres engine version")
	seed := flag.Int64("seed", 42, "random seed for warehouse data")
	warehouses := flag.Int("warehouses", 5, "number of warehouses to seed")
	scaleFactor := flag.Int("scale", 1, "pgbench scale factor")
	clients := flag.Int("clients", 4, "pgbench client count")
	duration := flag.Duration("duration", 15*time.Second, "pgbench run duration")
	workflowName := flag.String("workflow", "pgbench", "workflow to run (pgbench, recovery)")
	workflowID := flag.String("id", "", "workflow id (default: workflow name)")

	// recovery only.
	disruption := flag.String("disruption", "restart", "disruption to apply (restart, failover, crash)")
	repetitions := flag.Int("repetitions", 3, "how many times to disrupt")
	settle := flag.Duration("settle", 30*time.Second, "wait after each disruption before the next")
	highAvailability := flag.Bool("ha", false, "provision with a standby, which failover needs")
	probeImage := flag.String("probe-image", "dbtest/probe:dev", "prober image")
	benchImage := flag.String("bench-image", "dbtest/bench:dev", "bench image")
	probeInterval := flag.Duration("probe-interval", 0, "time between probe samples; 0 leaves the prober's default")
	flag.Parse()

	if *workflowID == "" {
		*workflowID = *workflowName
	}

	if *warehouses < 1 {
		slog.Error("warehouses must be > 0 (needed to seed data and compute the stock delta)")
		os.Exit(1)
	}

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

	// Build the workflow config.
	request := provider.ProvisionRequest{
		VCPU:             *vcpu,
		MemoryMiB:        *memoryMiB,
		DiskGiB:          *diskGiB,
		PostgresVersion:  *pgVersion,
		HighAvailability: *highAvailability,
	}
	workloadCfg := workload.Config{
		Seed:        *seed,
		Warehouses:  *warehouses,
		ScaleFactor: *scaleFactor,
		Clients:     *clients,
		Duration:    *duration,
	}

	// Both workflows take the same config shape; pick which one to start.
	var (
		wf  any
		cfg any
	)
	switch *workflowName {
	case "pgbench":
		wf = workflows.PgBenchWorkflow
		cfg = workflows.PgBenchWorkflowConfig{Provider: provider.ProviderName(*providerName), Request: request, Workload: workloadCfg}
	case "recovery":
		wf = workflows.RecoveryWorkflow
		cfg = workflows.RecoveryWorkflowConfig{
			Provider:      provider.ProviderName(*providerName),
			Request:       request,
			Disruption:    provider.Disruption(*disruption),
			Repetitions:   *repetitions,
			Settle:        *settle,
			ProbeImage:    *probeImage,
			BenchImage:    *benchImage,
			Scale:         *scaleFactor,
			ProbeInterval: *probeInterval,
		}
	default:
		slog.Error("unknown workflow", "workflow", *workflowName)
		os.Exit(1)
	}

	// Submit it and wait.
	we, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID:        *workflowID,
		TaskQueue: dbtemporal.TaskQueue,
	}, wf, cfg)
	if err != nil {
		slog.Error("start workflow failed", "error", err)
		os.Exit(1)
	}
	slog.Info("workflow started", "workflow_id", we.GetID(), "run_id", we.GetRunID())

	if err := we.Get(context.Background(), nil); err != nil {
		slog.Error("workflow failed", "error", err)
		os.Exit(1)
	}
	slog.Info("workflow completed")
}
