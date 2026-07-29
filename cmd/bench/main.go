// Command bench runs one workload against a database and prints the result as a
// single JSON line. It is the containerized load generator: locally a Docker
// container beside the database container, later an ECS task in the database's
// region. It never learns where it runs — it takes a DSN and a workload name,
// which is what makes the same image usable in both places.
//
// Telemetry is deliberately nil. Prometheus metrics are *scraped*, which only
// works for a long-lived process; a container that runs for a minute and exits
// would never be polled, and on Fargate it has no reachable address at all. So
// bench pushes its result out on stdout and the worker records the metrics.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/elenaochkina/dbtest/workload"
)

// resultPrefix marks the one machine-readable line on stdout. The harness scans
// container logs for it; everything else bench emits goes to stderr.
const resultPrefix = "DBTEST_RESULT "

// result is the contract between bench and the harness. Metrics comes straight
// from workload.Result.Metrics(), so bench stays workload-agnostic — it never
// needs to know what TPS is.
type result struct {
	Workload string             `json:"workload"`
	Phase    string             `json:"phase"` // "init" or "run"
	Metrics  map[string]float64 `json:"metrics,omitempty"`
	ElapsedS float64            `json:"elapsed_s"`
	Aborted  bool               `json:"aborted"`
	Error    string             `json:"error,omitempty"`
}

func main() {
	dsn := flag.String("dsn", "", "target database DSN (required)")
	name := flag.String("workload", "pgbench", "workload name (pgbench, warehouse)")
	initOnly := flag.Bool("init", false, "run only the workload's setup phase, then exit")
	scale := flag.Int("scale", 1, "pgbench scale factor")
	clients := flag.Int("clients", 4, "pgbench client count")
	duration := flag.Duration("duration", 30*time.Second, "how long the measured phase runs")
	seed := flag.Int64("seed", 42, "random seed for warehouse data")
	warehouses := flag.Int("warehouses", 5, "number of warehouses to seed")
	flag.Parse()

	if *dsn == "" {
		slog.Error("-dsn is required")
		os.Exit(2)
	}

	// SIGTERM is how a container is asked to stop. Cancelling the context makes
	// exec.CommandContext terminate the workload's child, so `docker stop` takes
	// pgbench down with us instead of leaving it orphaned.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := workload.Config{
		Seed:        *seed,
		Warehouses:  *warehouses,
		ScaleFactor: *scale,
		Clients:     *clients,
		Duration:    *duration,
	}

	res, err := run(ctx, *dsn, workload.WorkloadName(*name), *initOnly, cfg)
	// The result line is printed on every path, including failure: the database
	// dying underneath the workload is the expected outcome of a crash test, and
	// the harness still wants to see what happened.
	if err != nil {
		res.Aborted = true
		res.Error = err.Error()
	}
	emit(res)
	if err != nil {
		slog.Error("workload failed", "workload", *name, "error", err)
		os.Exit(1)
	}
}

// run resolves the workload and executes one phase of it. res is named so the
// deferred elapsed-time stamp lands on the value actually returned.
func run(ctx context.Context, dsn string, name workload.WorkloadName, initOnly bool, cfg workload.Config) (res result, err error) {
	res = result{Workload: string(name), Phase: "run"}
	if initOnly {
		res.Phase = "init"
	}

	w, err := workload.New(name, cfg)
	if err != nil {
		return res, err
	}

	start := time.Now()
	defer func() { res.ElapsedS = time.Since(start).Seconds() }()

	if initOnly {
		// A workload with no setup phase is not an error — there is simply
		// nothing to do.
		init, ok := w.(workload.Initializer)
		if !ok {
			slog.Info("workload has no setup phase", "workload", name)
			return res, nil
		}
		return res, init.Initialize(ctx, dsn, nil)
	}

	out, err := w.Run(ctx, dsn, nil)
	if err != nil {
		return res, err
	}
	if out != nil {
		res.Metrics = out.Metrics()
	}
	return res, nil
}

// emit writes the result line to stdout. A failure to marshal must not be
// silent — the harness would block waiting for a line that never comes.
func emit(res result) {
	payload, err := json.Marshal(res)
	if err != nil {
		slog.Error("marshal result", "error", err)
		return
	}
	fmt.Println(resultPrefix + string(payload))
}
