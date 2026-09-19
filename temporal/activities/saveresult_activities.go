package activities

import (
	"context"

	"github.com/elenaochkina/dbtest/pgbench"
	"github.com/elenaochkina/dbtest/probe"
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

// SaveDowntimeInput persists what the prober measured, one row per disruption.
type SaveDowntimeInput struct {
	RunID      uuid.UUID
	Provider   provider.ProviderName
	Disruption provider.Disruption
	Result     probe.Result
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

// SaveDowntimeResults writes one row per disruption.
func (a *SaveResultActivities) SaveDowntimeResults(ctx context.Context, input SaveDowntimeInput) error {
	rows := downtimeRows(input)
	return state.SaveDowntimeResults(ctx, a.statePool, input.RunID, rows, a.tel)
}

// downtimeRows pairs the two levels the prober records. Writable is the
// authority: it is the stronger condition, so every outage appears in it, while
// a disruption too brief to interrupt reads has no readable counterpart. Those
// rows carry a readable downtime of zero.
func downtimeRows(input SaveDowntimeInput) []state.DowntimeRow {
	readable := input.Result.Readable.Outages
	rows := make([]state.DowntimeRow, 0, len(input.Result.Writable.Outages))

	for i, w := range input.Result.Writable.Outages {
		row := state.DowntimeRow{
			Provider:           string(input.Provider),
			Disruption:         string(input.Disruption),
			Repetition:         i + 1,
			WritableDowntimeMs: w.DownMs,
			LostCommits:        w.LostCommits,
			ProbeIntervalMs:    input.Result.IntervalMs,
			ProbeFailures:      w.Failures,
			ProbeErrors:        w.Errors,
		}
		if r, ok := matchReadable(readable, w); ok {
			row.ReadableDowntimeMs = r.DownMs
		}
		rows = append(rows, row)
	}
	return rows
}

// matchReadable finds the readable outage that falls inside a writable one.
func matchReadable(readable []probe.Outage, w probe.Outage) (probe.Outage, bool) {
	for _, r := range readable {
		if r.FirstFailure.Before(w.LastOK) {
			continue
		}
		// A writable outage still open when the prober stopped has no end.
		if w.FirstOKAfter.IsZero() || !r.FirstFailure.After(w.FirstOKAfter) {
			return r, true
		}
	}
	return probe.Outage{}, false
}
