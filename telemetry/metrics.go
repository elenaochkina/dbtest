package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsConfig holds configuration for the Prometheus metrics server.
type MetricsConfig struct {
	MetricsPort int // port to serve /metrics on, e.g. 9090
}

// Metrics holds all Prometheus metric collectors and the HTTP server.
type Metrics struct {
	// ConnectionDuration tracks how long it takes to open a database connection.
	ConnectionDuration prometheus.Histogram

	// SeedRowsTotal counts rows inserted during seeding, labelled by table.
	SeedRowsTotal *prometheus.CounterVec

	// ChecksumDuration tracks how long a table checksum query takes.
	ChecksumDuration prometheus.Histogram

	// BenchmarkTPS holds the latest TPS from the most recent pgbench run, labelled by provider.
	BenchmarkTPS *prometheus.GaugeVec

	// BenchmarkLatencyAvgMs holds the latest average transaction latency (ms) from pgbench, labelled by provider.
	BenchmarkLatencyAvgMs *prometheus.GaugeVec

	// BenchmarkLatencyStddevMs holds the latest latency standard deviation (ms) from pgbench, labelled by provider.
	BenchmarkLatencyStddevMs *prometheus.GaugeVec

	// ProviderProvisionDuration tracks how long it takes to provision a database cluster, labelled by provider.
	ProviderProvisionDuration *prometheus.HistogramVec

	// ProviderDeprovisionTotal counts cluster deprovisions, labelled by provider.
	ProviderDeprovisionTotal *prometheus.CounterVec

	server *http.Server
}

// InitMetrics creates and registers all Prometheus metrics, then starts an
// HTTP server at /metrics in the background.
// Use Telemetry.Shutdown() to stop it cleanly.
func InitMetrics(cfg MetricsConfig) *Metrics {
	registry := prometheus.NewRegistry()

	connectionDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "dbtest_adapter_connect_duration_seconds",
		Help:    "How long it takes to open a connection to the database.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
	})
	seedRowsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dbtest_benchmark_seed_rows_total",
		Help: "Total number of rows inserted during seeding, by table.",
	}, []string{"table"})
	checksumDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "dbtest_validator_checksum_duration_seconds",
		Help:    "How long it takes to compute a table checksum.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
	})

	benchmarkTPS := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dbtest_benchmark_tps",
		Help: "Transactions per second from the most recent pgbench run.",
	}, []string{"provider"})
	benchmarkLatencyAvgMs := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dbtest_benchmark_latency_avg_ms",
		Help: "Average transaction latency in milliseconds from the most recent pgbench run.",
	}, []string{"provider"})
	benchmarkLatencyStddevMs := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dbtest_benchmark_latency_stddev_ms",
		Help: "Standard deviation of transaction latency in milliseconds from the most recent pgbench run.",
	}, []string{"provider"})

	providerProvisionDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dbtest_provider_provision_duration_seconds",
		Help:    "How long it takes to provision a database cluster.",
		Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60},
	}, []string{"provider"})
	providerDeprovisionTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dbtest_provider_deprovision_total",
		Help: "Total number of cluster deprovisions.",
	}, []string{"provider"})

	registry.MustRegister(connectionDuration, seedRowsTotal, checksumDuration, benchmarkTPS, benchmarkLatencyAvgMs, benchmarkLatencyStddevMs, providerProvisionDuration, providerDeprovisionTotal)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.MetricsPort),
		Handler: mux,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Warn("metrics server failed to start", "error", err)
		}
	}()

	return &Metrics{
		ConnectionDuration:        connectionDuration,
		SeedRowsTotal:             seedRowsTotal,
		ChecksumDuration:          checksumDuration,
		BenchmarkTPS:              benchmarkTPS,
		BenchmarkLatencyAvgMs:     benchmarkLatencyAvgMs,
		BenchmarkLatencyStddevMs:  benchmarkLatencyStddevMs,
		ProviderProvisionDuration: providerProvisionDuration,
		ProviderDeprovisionTotal:  providerDeprovisionTotal,
		server:                    server,
	}
}

// shutdown stops the HTTP metrics server cleanly.
func (m *Metrics) shutdown() {
	if err := m.server.Shutdown(context.Background()); err != nil {
		slog.Warn("metrics server shutdown error", "error", err)
	}
}
