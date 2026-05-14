package benchmark

import "github.com/prometheus/client_golang/prometheus"

// BenchMarkMetrics holds Prometheus metric collectors for the benchmark package.
type BenchMarkMetrics struct {
	// SeedRowsTotal counts the number of rows inserted during seeding.
	SeedRowsTotal *prometheus.CounterVec
}

// NewBenchMarkMetrics registers benchmark metrics with the given Prometheus registry
// and returns the populated BenchMarkMetrics struct.
func NewBenchMarkMetrics(reg prometheus.Registerer) *BenchMarkMetrics {
	seedRowsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dbtest_benchmark_seed_rows_total",
		Help: "Total number of rows inserted during seeding, by table.",
	}, []string{"table"})
	reg.MustRegister(seedRowsTotal)
	return &BenchMarkMetrics{SeedRowsTotal: seedRowsTotal}
}
