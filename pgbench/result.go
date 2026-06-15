package pgbench

import (
	"fmt"
	"io"
	"time"
)

// Result holds the summary output from a single pgbench run.
// It contains no database IDs — those belong to the state store.
type Result struct {
	TPS             float64
	LatencyAvgMs    float64
	LatencyStddevMs float64
	ScaleFactor     int
	Clients         int
	Duration        time.Duration
	Provider        string
}

// Metrics is the domain-neutral observability view over a Result, satisfying
// workload.Result. It carries the benchmark numbers as named values; typed
// persistence keeps using the struct fields directly.
func (r Result) Metrics() map[string]float64 {
	return map[string]float64{
		"tps":               r.TPS,
		"latency_avg_ms":    r.LatencyAvgMs,
		"latency_stddev_ms": r.LatencyStddevMs,
	}
}

// CompareResult holds two Results and their relative deltas.
type CompareResult struct {
	A           Result
	B           Result
	TPSDeltaPct float64 // positive = B is faster
	LatDeltaPct float64 // positive = B has lower latency (better)
}

// Compare computes relative TPS and latency deltas between a (baseline) and b.
func Compare(a, b Result) CompareResult {
	var tpsDelta, latDelta float64
	if a.TPS != 0 {
		tpsDelta = (b.TPS - a.TPS) / a.TPS * 100
	}
	if a.LatencyAvgMs != 0 {
		latDelta = (a.LatencyAvgMs - b.LatencyAvgMs) / a.LatencyAvgMs * 100
	}
	return CompareResult{A: a, B: b, TPSDeltaPct: tpsDelta, LatDeltaPct: latDelta}
}

// Print writes a human-readable comparison summary to w.
func (cr CompareResult) Print(w io.Writer) {
	faster := cr.A.Provider
	if cr.TPSDeltaPct > 0 {
		faster = cr.B.Provider
	}
	fmt.Fprintf(w, "provider A (%s): TPS=%.1f  latency_avg=%.2f ms\n", cr.A.Provider, cr.A.TPS, cr.A.LatencyAvgMs)
	fmt.Fprintf(w, "provider B (%s): TPS=%.1f  latency_avg=%.2f ms\n", cr.B.Provider, cr.B.TPS, cr.B.LatencyAvgMs)
	fmt.Fprintf(w, "TPS delta: %+.1f%%  latency delta: %+.1f%%  faster: %s\n", cr.TPSDeltaPct, cr.LatDeltaPct, faster)
}
