package pgbench_test

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/state"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPgbenchLocal(t *testing.T) {
	dsn := os.Getenv("DSN")
	if dsn == "" {
		t.Skip("DSN not set")
	}
	if _, err := exec.LookPath("pgbench"); err != nil {
		t.Skip("pgbench not installed")
	}

	ctx := context.Background()

	// Step 1: telemetry on a separate port to avoid conflicts with warehouse test.
	tel := telemetry.Init(telemetry.Config{
		Metrics: telemetry.MetricsConfig{MetricsPort: 9091},
	})
	defer tel.Shutdown()

	// Step 2: state store is optional — skip all persistence steps if STATE_DSN is unset.
	var statePool *pgxpool.Pool
	if stateDSN := os.Getenv("STATE_DSN"); stateDSN != "" {
		var err error
		statePool, err = state.Connect(stateDSN, tel)
		if err != nil {
			t.Fatalf("state.Connect: %v", err)
		}
		defer statePool.Close()
	}

	// Step 3: open a run row in the state store.
	var run *state.Run
	if statePool != nil {
		var err error
		run, err = state.StartRun(ctx, statePool, state.RunConfig{Seed: 0, Scenario: "pgbench-local"}, tel)
		if err != nil {
			t.Fatalf("state.StartRun: %v", err)
		}
		defer func() {
			if err := run.End(ctx, !t.Failed()); err != nil {
				t.Logf("run.End: %v", err)
			}
		}()
	}

	// Step 4: initialize pgbench tables and run the workload.
	result, err := pgbench.RunLocal(ctx, dsn, pgbench.Config{
		ScaleFactor: 1,
		Clients:     2,
		Duration:    5 * time.Second,
		Provider:    "local",
	}, tel)
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}

	// Step 5: basic sanity — pgbench must report non-zero throughput and latency.
	if result.TPS <= 0 {
		t.Errorf("expected TPS > 0, got %.2f", result.TPS)
	}
	if result.LatencyAvgMs <= 0 {
		t.Errorf("expected LatencyAvgMs > 0, got %.2f", result.LatencyAvgMs)
	}

	// Step 6: load the previous result before saving so the comparison is against a prior run.
	var prev *pgbench.Result
	if statePool != nil {
		var err error
		prev, err = state.GetLastBenchmarkResult(ctx, statePool, "local", tel)
		if err != nil {
			t.Fatalf("GetLastBenchmarkResult: %v", err)
		}
	}

	// Step 7: persist the current result.
	if statePool != nil {
		if err := state.SaveBenchmarkResult(ctx, statePool, run.ID, result, tel); err != nil {
			t.Fatalf("SaveBenchmarkResult: %v", err)
		}
	}

	// Step 8: compare against the previous run and warn on regression.
	if prev != nil {
		cmp := pgbench.Compare(*prev, result)
		cmp.Print(os.Stdout)
		if cmp.TPSDeltaPct < -20 {
			t.Logf("WARNING: TPS dropped %.1f%% vs previous run (%.1f → %.1f)",
				-cmp.TPSDeltaPct, prev.TPS, result.TPS)
		}
	}
}
