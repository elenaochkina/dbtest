// Command tester searches for the client count with the most throughput on a
// database whose schema is already built: HammerDB TPROC-C with -tool
// hammerdb, or pgbench's built-in TPC-B-like script with -tool pgbench.
// Before each timed run it deletes the rows earlier runs added, vacuums and
// analyzes the database and requests a checkpoint. It prints one JSON line
// per timed run and a final JSON line with the search result. A SELECT 1
// round-trip baseline, measured once before the first run, is added to every
// run's metrics.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/elenaochkina/dbtest/experiment/search"
	"github.com/elenaochkina/dbtest/hammerdb"
)

// tool is one benchmark tool's part of the search. The client and duration
// fields are its flag defaults.
type tool struct {
	workload   string // label of every output line
	throughput string // the trial metric the search maximizes
	minClients int
	maxClients int
	rampUp     time.Duration
	duration   time.Duration
	// prepare runs once before the first trial.
	prepare func(ctx context.Context) error
	// maintain runs before each trial and reports whether CHECKPOINT ran.
	maintain func(ctx context.Context) (bool, error)
	run      func(ctx context.Context, clients int, rampUp, duration time.Duration) (map[string]float64, error)
}

// trial is one timed run in tester's output.
type trial struct {
	Clients int `json:"clients"`
	// Repeat marks the final drift run of a client count already tried.
	Repeat   bool               `json:"repeat,omitempty"`
	Metrics  map[string]float64 `json:"metrics"`
	ElapsedS float64            `json:"elapsed_s"`
}

// result is the final line, the contract between tester and the harness.
type result struct {
	Workload string             `json:"workload"`
	Phase    string             `json:"phase"`
	Metrics  map[string]float64 `json:"metrics,omitempty"`
	Trials   []trial            `json:"trials"`
	ElapsedS float64            `json:"elapsed_s"`
	Aborted  bool               `json:"aborted"`
	Error    string             `json:"error,omitempty"`
}

func main() {
	dsn := flag.String("dsn", "", "target database DSN (required)")
	toolName := flag.String("tool", "hammerdb", "benchmark tool: hammerdb or pgbench")
	warehouses := flag.Int("warehouses", 32, "warehouses in the built schema, for hammerdb")
	minClients := flag.Int("min-clients", 0, "fewest clients to try (default 8 for hammerdb, 1 for pgbench)")
	maxClients := flag.Int("max-clients", 0, "most clients to try (default 128 for hammerdb, 256 for pgbench)")
	resolution := flag.Int("resolution", 4, "client gap at which a bisection stops")
	maxTrials := flag.Int("max-trials", 12, "most timed runs, not counting the drift run")
	tolerance := flag.Float64("tolerance", 0.03, "throughput fraction within which two runs count as equal")
	threshold := flag.Float64("threshold", 0.95, "fraction of peak throughput that marks saturation and decline")
	rampUp := flag.Duration("rampup", 0, "warm-up before each measured run (default 1m for hammerdb, 30s for pgbench)")
	duration := flag.Duration("duration", 0, "measured part of each run (default 3m for hammerdb, 2m for pgbench)")
	maintenance := flag.Bool("maintenance", true, "delete rows added by earlier runs, then VACUUM ANALYZE and CHECKPOINT, before each run")
	flag.Parse()

	if *dsn == "" {
		slog.Error("-dsn is required")
		os.Exit(2)
	}
	var tl tool
	switch *toolName {
	case "hammerdb":
		tl = newHammerDB(*dsn, *warehouses)
	case "pgbench":
		tl = newPgbench(*dsn)
	default:
		slog.Error("-tool must be hammerdb or pgbench", "tool", *toolName)
		os.Exit(2)
	}
	*rampUp = cmp.Or(*rampUp, tl.rampUp)
	*duration = cmp.Or(*duration, tl.duration)

	// SIGTERM is how a container is asked to stop.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := search.Config{
		MinClients: cmp.Or(*minClients, tl.minClients),
		MaxClients: cmp.Or(*maxClients, tl.maxClients),
		Resolution: *resolution,
		MaxTrials:  *maxTrials,
		Tolerance:  *tolerance,
		Threshold:  *threshold,
	}
	start := time.Now()
	res := result{Workload: tl.workload, Phase: "search"}
	baseline, err := hammerdb.RoundTrip(ctx, *dsn)
	if err != nil {
		res.Aborted = true
		res.Error = fmt.Sprintf("select 1 baseline: %v", err)
		emit(res)
		slog.Error("baseline failed", "error", err)
		os.Exit(1)
	}
	if err := tl.prepare(ctx); err != nil {
		res.Aborted = true
		res.Error = err.Error()
		emit(res)
		slog.Error("prepare failed", "error", err)
		os.Exit(1)
	}
	found, err := search.Run(ctx, cfg, func(ctx context.Context, clients int) (float64, error) {
		maintenanceSeconds := 0.0
		if *maintenance {
			maintenanceStart := time.Now()
			checkpointed, err := tl.maintain(ctx)
			if err != nil {
				return 0, fmt.Errorf("maintenance before %d clients: %w", clients, err)
			}
			maintenanceSeconds = time.Since(maintenanceStart).Seconds()
			slog.Info("maintenance done", "clients", clients, "seconds", maintenanceSeconds, "checkpoint", checkpointed)
		}
		runStart := time.Now()
		metrics, err := tl.run(ctx, clients, *rampUp, *duration)
		if err != nil {
			return 0, fmt.Errorf("%d clients: %w", clients, err)
		}
		current := trial{Clients: clients, Metrics: metrics, ElapsedS: time.Since(runStart).Seconds()}
		maps.Copy(current.Metrics, baseline)
		if *maintenance {
			current.Metrics["maintenance_s"] = maintenanceSeconds
		}
		for _, earlier := range res.Trials {
			current.Repeat = current.Repeat || earlier.Clients == clients
		}
		res.Trials = append(res.Trials, current)
		emit(struct {
			Workload string `json:"workload"`
			Phase    string `json:"phase"`
			trial
		}{tl.workload, "trial", current})
		return metrics[tl.throughput], nil
	})
	res.ElapsedS = time.Since(start).Seconds()
	if found.PeakClients > 0 {
		res.Metrics = map[string]float64{
			"peak_clients":          float64(found.PeakClients),
			"peak_" + tl.throughput: found.PeakThroughput,
			"saturation_clients":    float64(found.SaturationClients),
			"decline_clients":       float64(found.DeclineClients),
			"drift":                 found.Drift,
		}
		maps.Copy(res.Metrics, baseline)
	}
	// The result line is printed on every path, including failure.
	if err != nil {
		res.Aborted = true
		res.Error = err.Error()
	}
	emit(res)
	if err != nil {
		slog.Error("search failed", "error", err)
		os.Exit(1)
	}
}

// emit writes v to stdout as one JSON line. Logs go to stderr.
func emit(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal result", "error", err)
		return
	}
	fmt.Println(string(payload))
}
