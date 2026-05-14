package validator

import "github.com/prometheus/client_golang/prometheus"

// ValidatorMetrics holds Prometheus metric collectors for the validator package.
type ValidatorMetrics struct {
	// ChecksumDuration tracks how long it takes to compute a table checksum.
	ChecksumDuration prometheus.Histogram
}

// NewValidatorMetrics registers validator metrics with the given Prometheus registry
// and returns the populated ValidatorMetrics struct.
func NewValidatorMetrics(reg prometheus.Registerer) *ValidatorMetrics {
	checksumDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "dbtest_validator_checksum_duration_seconds",
		Help:    "How long it takes to compute a table checksum.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
	})
	reg.MustRegister(checksumDuration)
	return &ValidatorMetrics{ChecksumDuration: checksumDuration}
}
