package stack

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	sdkworker "go.temporal.io/sdk/worker"

	"github.com/elenaochkina/dbtest/state"
	"github.com/elenaochkina/dbtest/telemetry"
	dbtemporal "github.com/elenaochkina/dbtest/temporal"
	dbworker "github.com/elenaochkina/dbtest/temporal/worker"
)

// workerService runs the Temporal worker in this process.
//
//	stateDB, the Temporal server and the telemetry service, which must be up before.
type workerService struct {
	stateDB   *stateDB
	temporal  *temporalServer
	telemetry *telemetryService
	logs      slog.Handler

	dsn     string
	address string
	tel     *telemetry.Telemetry

	statePool      *pgxpool.Pool
	temporalClient client.Client
	worker         sdkworker.Worker
}

func newWorkerService(stateDB *stateDB, temporal *temporalServer, telemetry *telemetryService, logs slog.Handler) *workerService {
	return &workerService{stateDB: stateDB, temporal: temporal, telemetry: telemetry, logs: logs}
}

// starts the worker unless it is running, then checks its connections.
// It returns the task queue.
func (s *workerService) start(ctx context.Context) (string, error) {
	if s.tel == nil {
		s.dsn = s.stateDB.dsn
		s.address = s.temporal.address
		s.tel = s.telemetry.tel
	}
	if s.worker == nil {
		statePool, err := state.Connect(s.dsn, s.tel)
		if err != nil {
			return "", err
		}
		temporalClient, err := client.DialContext(ctx, client.Options{
			HostPort: s.address,
			Logger:   log.NewStructuredLogger(slog.New(s.logs)),
		})
		if err != nil {
			statePool.Close()
			return "", fmt.Errorf("temporal dial: %w", err)
		}
		worker := dbworker.New(temporalClient, statePool, s.tel)
		if err := worker.Start(); err != nil {
			temporalClient.Close()
			statePool.Close()
			return "", fmt.Errorf("worker start: %w", err)
		}
		s.statePool, s.temporalClient, s.worker = statePool, temporalClient, worker
	}
	if err := s.check(ctx); err != nil {
		return "", err
	}
	return "task queue " + dbtemporal.TaskQueue, nil
}

// check pings the stateDB and asks the Temporal server for its health.
func (s *workerService) check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.statePool.Ping(ctx); err != nil {
		return fmt.Errorf("state db: %w", err)
	}
	if _, err := s.temporalClient.CheckHealth(ctx, &client.CheckHealthRequest{}); err != nil {
		return fmt.Errorf("temporal: %w", err)
	}
	return nil
}

// stops the worker and closes its connections.
func (s *workerService) stop() {
	if s.worker == nil {
		return
	}
	s.worker.Stop()
	s.temporalClient.Close()
	s.statePool.Close()
	s.worker = nil
}
