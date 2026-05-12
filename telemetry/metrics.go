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

// Metrics holds the Prometheus registry, all metric collectors, and the HTTP server.
// Other packages record observations directly via the exported fields.
type Metrics struct {
	// AdapterConnectDuration tracks how long it takes to connect to the database.
	AdapterConnectDuration prometheus.Histogram

	// SeedRowsTotal counts how many rows have been inserted, broken down by table.
	// Example: SeedRowsTotal.WithLabelValues("warehouse").Inc()
	SeedRowsTotal *prometheus.CounterVec

	// ValidatorChecksumDuration tracks how long checksum queries take.
	ValidatorChecksumDuration prometheus.Histogram

	// internal fields — not used outside this package
	registry *prometheus.Registry
	server   *http.Server
}

// InitMetrics creates a Prometheus registry, registers all metrics,
// and starts an HTTP server at /metrics in the background.
// Use defer tel.Shutdown() on the parent Telemetry to stop the server cleanly.
func InitMetrics(cfg MetricsConfig) *Metrics {
	// --- Prometheus registry ---
	// use a custom registry (not the global default) to avoid conflicts
	// when running multiple tests
	registry := prometheus.NewRegistry()

	// --- register metrics ---
	// histogram buckets in seconds — covers fast (1ms) to slow (1s) operations
	durationBuckets := []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0}

	adapterConnectDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "dbtest_adapter_connect_duration_seconds",
		Help:    "How long it takes to open a connection to the database.",
		Buckets: durationBuckets,
	})

	seedRowsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dbtest_seed_rows_total",
		Help: "Total number of rows inserted during seeding, by table name.",
	}, []string{"table"}) // "table" is the label dimension

	validatorChecksumDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "dbtest_validation_checksum_duration_seconds",
		Help:    "How long it takes to compute a table checksum.",
		Buckets: durationBuckets,
	})

	// register all metrics with the registry
	registry.MustRegister(
		adapterConnectDuration,
		seedRowsTotal,
		validatorChecksumDuration,
	)

	// --- HTTP server ---
	mux := http.NewServeMux()

	// promhttp.HandlerFor implements http.Handler (ServeHTTP method)
	// it reads the registry and formats metrics as Prometheus text
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.MetricsPort),
		Handler: mux,
	}

	// run in background — does not block the test
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// port already in use or other error — log warning, don't crash
			slog.Warn("metrics server failed to start", "error", err)
		}
	}()

	return &Metrics{
		AdapterConnectDuration:    adapterConnectDuration,
		SeedRowsTotal:             seedRowsTotal,
		ValidatorChecksumDuration: validatorChecksumDuration,
		registry:                  registry,
		server:                    server,
	}
}

// Shutdown stops the HTTP metrics server cleanly, waiting for in-flight
// requests to finish before returning.
func (m *Metrics) shutdown() {
	if err := m.server.Shutdown(context.Background()); err != nil {
		slog.Warn("metrics server shutdown error", "error", err)
	}
}

