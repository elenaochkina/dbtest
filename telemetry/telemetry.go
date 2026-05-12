package telemetry

// Config holds the configuration for the full telemetry setup.
type Config struct {
	Log     LogConfig
	Metrics MetricsConfig
}

// Telemetry is the top-level handle returned by Init.
// It groups all observability components so new ones (traces, profiles) can be added later.
type Telemetry struct {
	Metrics *Metrics
	// reserved for future components
}

// Init sets up structured logging and Prometheus metrics.
// Call once at the start of your program. Use defer tel.Shutdown() to clean up.
func Init(cfg Config) *Telemetry {
	InitLogging(cfg.Log)
	return &Telemetry{
		Metrics: InitMetrics(cfg.Metrics),
	}
}

// Shutdown cleanly stops all telemetry components.
func (t *Telemetry) Shutdown() {
	t.Metrics.shutdown()
}

