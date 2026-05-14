package pgadapter

import "github.com/prometheus/client_golang/prometheus"

// Metrics holds Prometheus metric collectors for the pgadapter package.
type PGAdapterMetrics struct {
	// ConnectionDuration tracks how long it takes to open a connection to the database.
	ConnectionDuration prometheus.Histogram
}

// NewAdapterMetrics registers pgadapter metrics with the given Prometheus registry
// and returns the populated PGAdapterMetrics struct.
func NewAdapterMetrics(reg prometheus.Registerer) *PGAdapterMetrics {
	connectionDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "dbtest_adapter_connect_duration_seconds",
		Help:    "How long it takes to open a connection to the database.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
	})
	reg.MustRegister(connectionDuration)
	return &PGAdapterMetrics{
		ConnectionDuration: connectionDuration,
	}
}
