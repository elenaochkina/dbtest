package provider

// Disruption is a way of interrupting a running cluster. Which ones are
// available depends on the provider and on the topology it was asked for.
type Disruption string

const (
	// Restart shuts the server down cleanly and starts it again.
	Restart Disruption = "restart"

	// Failover promotes a standby, so it needs the high availability option.
	Failover Disruption = "failover"

	// Crash ends the server without a clean shutdown, leaving WAL to replay.
	// Only Docker offers it so far.
	Crash Disruption = "crash"
)
