package console

import (
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/workflows"
)

// defaultRecovery returns the recovery workflow input with cmd/starter's
// defaults.
func defaultRecovery() workflows.RecoveryWorkflowConfig {
	return workflows.RecoveryWorkflowConfig{
		Provider:    provider.Docker,
		Request:     provider.ProvisionRequest{VCPU: 2, MemoryMiB: 2048, PostgresVersion: "18"},
		Disruption:  provider.Restart,
		Repetitions: 3,
		Settle:      30 * time.Second,
		ProbeImage:  "dbtest/probe:dev",
		BenchImage:  "dbtest/bench:dev",
		Scale:       1,
	}
}

// Starts a recovery workflow with cfg and logs an error if it cannot start.
func startRecovery(start func(workflows.RecoveryWorkflowConfig) (string, error), cfg workflows.RecoveryWorkflowConfig) tea.Cmd {
	return func() tea.Msg {
		if _, err := start(cfg); err != nil {
			slog.Error("start recovery failed", "error", err)
		}
		return nil
	}
}
