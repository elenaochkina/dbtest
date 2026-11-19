package pgbench

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/elenaochkina/dbtest/telemetry"
)

// Config holds the parameters for a pgbench run.
type Config struct {
	ScaleFactor int
	Clients     int
	// Threads is pgbench's worker thread count. 0 uses one thread per CPU, at
	// most one per client.
	Threads int
	// WarmUp runs before Duration and is left out of the result.
	WarmUp   time.Duration
	Duration time.Duration
	Provider string // label for metrics and logs only — does not affect pgbench behavior
}

// Initialize runs `pgbench -i -s <ScaleFactor> <dsn>`, creating and populating
// the pgbench tables in the target database.
func Initialize(ctx context.Context, dsn string, cfg Config) error {
	out, err := exec.CommandContext(ctx,
		"pgbench", "-i", "-s", strconv.Itoa(cfg.ScaleFactor), dsn,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pgbench initialize: %w\n%s", err, out)
	}
	return nil
}

// Run executes the built-in TPC-B-like script against dsn for WarmUp plus
// Duration with prepared statements, -P 5 progress and a 5% transaction log
// sample. TPS is the mean of the progress intervals after WarmUp, and latency
// comes from the sampled transactions that started after WarmUp.
func Run(ctx context.Context, dsn string, cfg Config, tel *telemetry.Telemetry) (Result, error) {
	threads := cfg.Threads
	if threads == 0 {
		threads = runtime.GOMAXPROCS(0)
	}
	threads = min(threads, cfg.Clients)
	logDir, err := os.MkdirTemp("", "pgbench-log-")
	if err != nil {
		return Result{}, fmt.Errorf("pgbench run: %w", err)
	}
	defer os.RemoveAll(logDir)
	prefix := filepath.Join(logDir, "pgbench_log")
	out, err := exec.CommandContext(ctx,
		"pgbench",
		"-c", strconv.Itoa(cfg.Clients),
		"-j", strconv.Itoa(threads),
		"-T", strconv.Itoa(int((cfg.WarmUp + cfg.Duration).Seconds())),
		"-M", "prepared",
		"-P", "5",
		"-l", "--sampling-rate", "0.05", "--log-prefix", prefix,
		"--no-vacuum",
		dsn,
	).CombinedOutput()
	if err != nil {
		return Result{}, fmt.Errorf("pgbench run: %w\n%s", err, out)
	}

	tps, noise, err := measuredTPS(string(out), cfg.WarmUp)
	if err != nil {
		return Result{}, err
	}
	logs, err := readLogs(prefix)
	if err != nil {
		return Result{}, err
	}
	latencies, err := measuredLatencies(logs, cfg.WarmUp)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		TPS:          tps,
		TPSNoise:     noise,
		LatencyP50Ms: percentile(latencies, 0.50),
		LatencyP95Ms: percentile(latencies, 0.95),
		LatencyP99Ms: percentile(latencies, 0.99),
		ScaleFactor:  cfg.ScaleFactor,
		Clients:      cfg.Clients,
		Threads:      threads,
		Duration:     cfg.Duration,
		Provider:     cfg.Provider,
	}
	result.LatencyAvgMs, result.LatencyStddevMs = meanAndStddev(latencies)

	if tel != nil {
		tel.Metrics.BenchmarkTPS.WithLabelValues(cfg.Provider).Set(result.TPS)
		tel.Metrics.BenchmarkLatencyAvgMs.WithLabelValues(cfg.Provider).Set(result.LatencyAvgMs)
		tel.Metrics.BenchmarkLatencyStddevMs.WithLabelValues(cfg.Provider).Set(result.LatencyStddevMs)
		tel.Logger.With("package", "pgbench").Info("benchmark complete",
			"provider", cfg.Provider,
			"tps", result.TPS,
			"latency_avg_ms", result.LatencyAvgMs,
			"latency_p95_ms", result.LatencyP95Ms,
			"clients", cfg.Clients,
			"threads", threads,
			"duration", cfg.Duration,
		)
	}

	return result, nil
}

// progressRe matches a -P line: the seconds since the run started and the
// TPS of the interval ending then.
var progressRe = regexp.MustCompile(`(?m)^progress: ([\d.]+) s, ([\d.]+) tps`)

// measuredTPS returns the mean TPS of the progress intervals that start at or
// after warmUp, and their standard deviation as a fraction of that mean.
func measuredTPS(output string, warmUp time.Duration) (float64, float64, error) {
	var rates []float64
	for _, match := range progressRe.FindAllStringSubmatch(output, -1) {
		end, _ := strconv.ParseFloat(match[1], 64)
		rate, _ := strconv.ParseFloat(match[2], 64)
		if end-5 >= warmUp.Seconds() {
			rates = append(rates, rate)
		}
	}
	if len(rates) == 0 {
		return 0, 0, fmt.Errorf("pgbench output: no progress after %s warm-up\n%s", warmUp, output)
	}
	mean, stddev := meanAndStddev(rates)
	if mean == 0 {
		return 0, 0, nil
	}
	return mean, stddev / mean, nil
}

// readLogs returns the contents of each transaction log file pgbench wrote at
// prefix: one per thread.
func readLogs(prefix string) ([]string, error) {
	files, err := filepath.Glob(prefix + ".*")
	if err != nil {
		return nil, fmt.Errorf("pgbench log: %w", err)
	}
	logs := make([]string, 0, len(files))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("pgbench log: %w", err)
		}
		logs = append(logs, string(data))
	}
	return logs, nil
}

// transaction is one line of a pgbench transaction log.
type transaction struct {
	start     float64 // Unix seconds
	latencyMs float64
}

// parseLog reads transaction log lines: client, transaction number, latency
// in microseconds, script, and the end time as Unix seconds and microseconds.
// Failed and skipped transactions are left out.
func parseLog(text string) []transaction {
	var transactions []transaction
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		latency, latencyErr := strconv.ParseFloat(fields[2], 64)
		seconds, secondsErr := strconv.ParseFloat(fields[4], 64)
		micros, microsErr := strconv.ParseFloat(fields[5], 64)
		if errors.Join(latencyErr, secondsErr, microsErr) != nil {
			continue
		}
		end := seconds + micros/1e6
		transactions = append(transactions, transaction{start: end - latency/1e6, latencyMs: latency / 1e3})
	}
	return transactions
}

// measuredLatencies returns, sorted, the latency in milliseconds of each
// logged transaction that started at least warmUp after the first.
func measuredLatencies(logs []string, warmUp time.Duration) ([]float64, error) {
	var transactions []transaction
	for _, text := range logs {
		transactions = append(transactions, parseLog(text)...)
	}
	if len(transactions) == 0 {
		return nil, errors.New("pgbench log: no transactions")
	}
	first := slices.MinFunc(transactions, func(a, b transaction) int { return cmp.Compare(a.start, b.start) })
	var latencies []float64
	for _, t := range transactions {
		if t.start >= first.start+warmUp.Seconds() {
			latencies = append(latencies, t.latencyMs)
		}
	}
	if len(latencies) == 0 {
		return nil, fmt.Errorf("pgbench log: no transactions after %s warm-up", warmUp)
	}
	slices.Sort(latencies)
	return latencies, nil
}

// meanAndStddev returns the mean and population standard deviation of values.
func meanAndStddev(values []float64) (float64, float64) {
	var sum, squares float64
	for _, value := range values {
		sum += value
	}
	mean := sum / float64(len(values))
	for _, value := range values {
		squares += (value - mean) * (value - mean)
	}
	return mean, math.Sqrt(squares / float64(len(values)))
}

// percentile returns the nearest-rank fraction quantile of sorted values.
func percentile(sorted []float64, fraction float64) float64 {
	return sorted[max(0, int(math.Ceil(fraction*float64(len(sorted))))-1)]
}
