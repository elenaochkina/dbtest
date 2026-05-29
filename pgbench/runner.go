package pgbench

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/elenaochkina/dbtest/telemetry"
)

// Config holds the parameters for a pgbench run.
type Config struct {
	ScaleFactor int
	Clients     int
	Duration    time.Duration
	Provider    string // label for metrics and logs only — does not affect pgbench behavior
}

// Initialize runs `pgbench -i -s <ScaleFactor> <dsn>`, creating and populating
// the pgbench tables in the target database. Safe to call repeatedly — pgbench
// drops and recreates the tables each time. Does not emit metrics.
func Initialize(ctx context.Context, dsn string, cfg Config) error {
	out, err := exec.CommandContext(ctx,
		"pgbench", "-i", "-s", strconv.Itoa(cfg.ScaleFactor), dsn,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pgbench initialize: %w\n%s", err, out)
	}
	return nil
}

// RunLocal initializes pgbench tables then runs the standard TPC-B workload
// with -c <Clients> -T <seconds> -P 5 --no-vacuum against dsn.
// Emits BenchmarkTPS, BenchmarkLatencyAvgMs, and BenchmarkLatencyStddevMs
// metrics and logs "benchmark complete" if tel is not nil.
func RunLocal(ctx context.Context, dsn string, cfg Config, tel *telemetry.Telemetry) (Result, error) {
	if err := Initialize(ctx, dsn, cfg); err != nil {
		return Result{}, err
	}

	seconds := strconv.Itoa(int(cfg.Duration.Seconds()))
	out, err := exec.CommandContext(ctx,
		"pgbench",
		"-c", strconv.Itoa(cfg.Clients),
		"-T", seconds,
		"-P", "5",
		"--no-vacuum",
		dsn,
	).CombinedOutput()
	if err != nil {
		return Result{}, fmt.Errorf("pgbench run: %w\n%s", err, out)
	}

	result, err := parsePgbenchOutput(string(out), cfg)
	if err != nil {
		return Result{}, err
	}

	if tel != nil {
		tel.Metrics.BenchmarkTPS.WithLabelValues(cfg.Provider).Set(result.TPS)
		tel.Metrics.BenchmarkLatencyAvgMs.WithLabelValues(cfg.Provider).Set(result.LatencyAvgMs)
		tel.Metrics.BenchmarkLatencyStddevMs.WithLabelValues(cfg.Provider).Set(result.LatencyStddevMs)
		tel.Logger.With("package", "pgbench").Info("benchmark complete",
			"provider", cfg.Provider,
			"tps", result.TPS,
			"latency_avg_ms", result.LatencyAvgMs,
			"clients", cfg.Clients,
			"duration", cfg.Duration,
		)
	}

	return result, nil
}

// tpsRe matches the TPS line that excludes connection setup time.
// pgbench prints two TPS lines; we always want the one that reflects
// steady-state throughput, not the one inflated by connection overhead.
// Two variants exist across Postgres versions:
//
//	tps = N.N (excluding connections establishing)   ← older versions
//	tps = N.N (without initial connection time)      ← newer versions
var tpsRe = regexp.MustCompile(
	`tps\s*=\s*([\d.]+)\s*\((?:excluding connections establishing|without initial connection time)\)`,
)

// latAvgRe matches: latency average = N.N ms
var latAvgRe = regexp.MustCompile(`latency average\s*=\s*([\d.]+)\s*ms`)

// latStddevRe matches: latency stddev = N.N ms (optional — not present in all versions)
var latStddevRe = regexp.MustCompile(`latency stddev\s*=\s*([\d.]+)\s*ms`)

func parsePgbenchOutput(output string, cfg Config) (Result, error) {
	tpsMatch := tpsRe.FindStringSubmatch(output)
	if tpsMatch == nil {
		return Result{}, fmt.Errorf("pgbench output: TPS line not found\n%s", output)
	}
	tps, err := strconv.ParseFloat(tpsMatch[1], 64)
	if err != nil {
		return Result{}, fmt.Errorf("pgbench output: parse TPS %q: %w", tpsMatch[1], err)
	}

	var latAvg float64
	if m := latAvgRe.FindStringSubmatch(output); m != nil {
		latAvg, _ = strconv.ParseFloat(m[1], 64)
	}

	var latStddev float64
	if m := latStddevRe.FindStringSubmatch(output); m != nil {
		latStddev, _ = strconv.ParseFloat(m[1], 64)
	}

	return Result{
		TPS:             tps,
		LatencyAvgMs:    latAvg,
		LatencyStddevMs: latStddev,
		ScaleFactor:     cfg.ScaleFactor,
		Clients:         cfg.Clients,
		Duration:        cfg.Duration,
		Provider:        cfg.Provider,
	}, nil
}
