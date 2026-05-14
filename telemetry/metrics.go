package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)
// MetricsConfig holds configuration for the Prometheus metrics server

type MetricsConfig struct {
	MetricsPort int // port to serve /metrics on, e.g. 9090
}

// Metrics holds the shared Prometheus registry and the HTTP server.
// Each package registers its own metrics using Registry.
type Metrics struct {
	// Registry is the shared Prometheus registry. Pass it to each package's
	// NewMetrics() function to register package-specific metric collectors.
	Registry *prometheus.Registry

	// internal field — not used outside this package
	server *http.Server
}

// InitMetrics creates a Prometheus registry, registers all metrics,
// and starts an HTTP server at /metrics in the background.
// Use defer tel.Shutdown() on the parent Telemetry to stop the server cleanly.
func InitMetrics(cfg MetricsConfig) *Metrics {
	// --- Prometheus registry ---
	// use a custom registry (not the global default) to avoid conflicts
	// when running multiple tests
	registry := prometheus.NewRegistry()

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
		Registry:  registry,
		server:    server,
	}
}

// Shutdown stops the HTTP metrics server cleanly, waiting for in-flight
// requests to finish before returning.
func (m *Metrics) shutdown() {
	if err := m.server.Shutdown(context.Background()); err != nil {
		slog.Warn("metrics server shutdown error", "error", err)
	}
}

