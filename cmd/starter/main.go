package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/elenaochkina/dbtest/provider"
	dbtemporal "github.com/elenaochkina/dbtest/temporal"

	"go.temporal.io/sdk/client"
)

// starter triggers one ProvisionTeardownWorkflow execution and waits for it.
// It talks only to the Temporal server — the worker owns the providers and any
// infrastructure the activities touch.
func main() {
	providerName := flag.String("provider", "docker", "provider name (docker, aws)")
	vcpu := flag.Float64("vcpu", 2, "cluster vCPU")
	memoryMiB := flag.Int("memory-mib", 2048, "cluster memory (MiB)")
	diskGiB := flag.Int("disk-gib", 0, "cluster disk (GiB); 0 = provider default")
	pgVersion := flag.String("pg-version", "16", "postgres engine version")
	workflowID := flag.String("id", "provision-teardown", "workflow id")
	flag.Parse()

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

	cfg := dbtemporal.Config{
		Provider: provider.ProviderName(*providerName),
		Request: provider.ProvisionRequest{
			VCPU:            *vcpu,
			MemoryMiB:       *memoryMiB,
			DiskGiB:         *diskGiB,
			PostgresVersion: *pgVersion,
		},
	}

	we, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID:        *workflowID,
		TaskQueue: dbtemporal.TaskQueue,
	}, dbtemporal.ProvisionTeardownWorkflow, cfg)
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
