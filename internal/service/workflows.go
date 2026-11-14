package service

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

	dbtemporal "github.com/elenaochkina/dbtest/temporal"
	"github.com/elenaochkina/dbtest/temporal/workflows"
)

// StartRecovery starts a recovery workflow with cfg and returns its ID. It
// logs when the workflow starts, completes or fails.
func (l *Launcher) StartRecovery(cfg workflows.RecoveryWorkflowConfig) (string, error) {
	select {
	case <-l.workerUp:
	default:
		return "", errors.New("the worker is not up yet")
	}
	id := fmt.Sprintf("recovery-%s-%s-%s", uuid.NewString(), cfg.Cloud, cfg.Provider)
	run, err := l.worker.temporalClient.ExecuteWorkflow(l.ctx, client.StartWorkflowOptions{
		ID:        id,
		TaskQueue: dbtemporal.TaskQueue,
	}, workflows.RecoveryWorkflow, cfg)
	if err != nil {
		return "", fmt.Errorf("start %s: %w", id, err)
	}
	slog.Info("workflow started", "workflow_id", id)
	l.wait.Go(func() {
		if err := run.Get(l.ctx, nil); err != nil {
			slog.Error("workflow failed", "workflow_id", id, "error", err)
			return
		}
		slog.Info("workflow completed", "workflow_id", id)
	})
	return id, nil
}
