// Command tester searches for the client count with the most TPROC-C
// throughput on a database whose schema is already built. Before each timed
// run it deletes the rows earlier runs added, vacuums and analyzes the tpcc
// database and requests a checkpoint. It
// prints one JSON line per timed run and a final JSON line with the search
// result. A SELECT 1
// round-trip baseline, measured once before the first run, is added to every
// run's metrics.
package main

import (
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

// probe is one timed run in tester's output.
type probe struct {
	Clients int `json:"clients"`
	// Repeat marks the final drift run of a client count already probed.
	Repeat   bool               `json:"repeat,omitempty"`
	Metrics  map[string]float64 `json:"metrics"`
	ElapsedS float64            `json:"elapsed_s"`
}

// result is the final line, the contract between tester and the harness.
type result struct {
	Workload string             `json:"workload"`
	Phase    string             `json:"phase"`
	Metrics  map[string]float64 `json:"metrics,omitempty"`
	Probes   []probe            `json:"probes"`
	ElapsedS float64            `json:"elapsed_s"`
	Aborted  bool               `json:"aborted"`
	Error    string             `json:"error,omitempty"`
}

func main() {
	dsn := flag.String("dsn", "", "target database DSN (required)")
	warehouses := flag.Int("warehouses", 32, "warehouses in the built schema")
	minClients := flag.Int("min-clients", 8, "fewest virtual users to try")
	maxClients := flag.Int("max-clients", 128, "most virtual users to try")
	resolution := flag.Int("resolution", 4, "client gap at which a bisection stops")
	maxProbes := flag.Int("max-probes", 12, "most timed runs, not counting the drift run")
	tolerance := flag.Float64("tolerance", 0.03, "NOPM fraction within which two runs count as equal")
	threshold := flag.Float64("threshold", 0.95, "fraction of peak NOPM that marks saturation and decline")
	rampUp := flag.Duration("rampup", time.Minute, "ramp-up before each measured run")
	duration := flag.Duration("duration", 3*time.Minute, "measured part of each run")
	maintenance := flag.Bool("maintenance", true, "delete rows added by earlier runs, then VACUUM ANALYZE and CHECKPOINT, before each run")
	flag.Parse()

	if *dsn == "" {
		slog.Error("-dsn is required")
		os.Exit(2)
	}

	// SIGTERM is how a container is asked to stop.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := search.Config{
		MinClients: *minClients,
		MaxClients: *maxClients,
		Resolution: *resolution,
		MaxProbes:  *maxProbes,
		Tolerance:  *tolerance,
		Threshold:  *threshold,
	}
	start := time.Now()
	res := result{Workload: "tpcc", Phase: "search"}
	baseline, err := hammerdb.RoundTrip(ctx, *dsn)
	if err != nil {
		res.Aborted = true
		res.Error = fmt.Sprintf("select 1 baseline: %v", err)
		emit(res)
		slog.Error("baseline failed", "error", err)
		os.Exit(1)
	}
	var base hammerdb.Baseline
	if *maintenance {
		base, err = hammerdb.Snapshot(ctx, *dsn)
		if err != nil {
			res.Aborted = true
			res.Error = fmt.Sprintf("snapshot: %v", err)
			emit(res)
			slog.Error("snapshot failed", "error", err)
			os.Exit(1)
		}
	}
	found, err := search.Run(ctx, cfg, func(ctx context.Context, clients int) (float64, error) {
		maintenanceSeconds := 0.0
		if *maintenance {
			maintenanceStart := time.Now()
			checkpointed, err := hammerdb.Maintain(ctx, *dsn, base)
			if err != nil {
				return 0, fmt.Errorf("maintenance before %d clients: %w", clients, err)
			}
			maintenanceSeconds = time.Since(maintenanceStart).Seconds()
			slog.Info("maintenance done", "clients", clients, "seconds", maintenanceSeconds, "checkpoint", checkpointed)
		}
		runStart := time.Now()
		out, err := hammerdb.Run(ctx, *dsn, hammerdb.Config{
			Warehouses: *warehouses,
			Clients:    clients,
			RampUp:     *rampUp,
			Duration:   *duration,
		})
		if err != nil {
			return 0, fmt.Errorf("%d clients: %w", clients, err)
		}
		p := probe{Clients: clients, Metrics: out.Metrics(), ElapsedS: time.Since(runStart).Seconds()}
		maps.Copy(p.Metrics, baseline)
		if *maintenance {
			p.Metrics["maintenance_s"] = maintenanceSeconds
		}
		for _, earlier := range res.Probes {
			p.Repeat = p.Repeat || earlier.Clients == clients
		}
		res.Probes = append(res.Probes, p)
		emit(struct {
			Workload string `json:"workload"`
			Phase    string `json:"phase"`
			probe
		}{"tpcc", "probe", p})
		return out.NOPM, nil
	})
	res.ElapsedS = time.Since(start).Seconds()
	if found.PeakClients > 0 {
		res.Metrics = map[string]float64{
			"peak_clients":       float64(found.PeakClients),
			"peak_nopm":          found.PeakThroughput,
			"saturation_clients": float64(found.SaturationClients),
			"decline_clients":    float64(found.DeclineClients),
			"drift":              found.Drift,
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
