package activities

import (
	"context"

	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/state"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StartRunInput opens a runs row recording this benchmark execution.
type StartRunInput struct {
	Scenario string
	Seed     int64
	Provider provider.ProviderName
}

// SaveResultInput persists one pgbench result.
type SaveResultInput struct {
	RunID  uuid.UUID
	Result pgbench.Result
}

// EndRunInput closes a runs row, recording whether the run passed.
type EndRunInput struct {
	RunID  uuid.UUID
	Passed bool
}

// SaveResultActivities persist the run lifecycle and results to the state DB.
type SaveResultActivities struct {
	statePool *pgxpool.Pool
	tel       *telemetry.Telemetry
}

func NewSaveResultActivities(statePool *pgxpool.Pool, tel *telemetry.Telemetry) *SaveResultActivities {
	return &SaveResultActivities{statePool: statePool, tel: tel}
}

func (a *SaveResultActivities) StartRun(ctx context.Context, input StartRunInput) (uuid.UUID, error) {
	run, err := state.StartRun(ctx, a.statePool, state.RunConfig{
		Seed:     input.Seed,
		Scenario: input.Scenario,
		Provider: string(input.Provider),
	}, a.tel)
	if err != nil {
		return uuid.UUID{}, err
	}
	return run.ID, nil
}

func (a *SaveResultActivities) SaveResult(ctx context.Context, input SaveResultInput) error {
	return state.SaveBenchmarkResult(ctx, a.statePool, input.RunID, input.Result, a.tel)
}

func (a *SaveResultActivities) EndRun(ctx context.Context, input EndRunInput) error {
	run := &state.Run{Pool: a.statePool, ID: input.RunID, Logger: a.tel.Logger}
	return run.End(ctx, input.Passed)
}
