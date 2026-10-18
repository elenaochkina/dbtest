package activities

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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
	// DisruptedAt is when each disruption was applied, in order.
	DisruptedAt []time.Time
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

// StateDBActivities cover every write to the state DB.
type StateDBActivities struct {
	statePool *pgxpool.Pool
	tel       *telemetry.Telemetry
}

func NewStateDBActivities(statePool *pgxpool.Pool, tel *telemetry.Telemetry) *StateDBActivities {
	return &StateDBActivities{statePool: statePool, tel: tel}
}

func (a *StateDBActivities) StartRun(ctx context.Context, input StartRunInput) (uuid.UUID, error) {
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

func (a *StateDBActivities) SaveResult(ctx context.Context, input SaveResultInput) error {
	return state.SaveBenchmarkResult(ctx, a.statePool, input.RunID, input.Result, a.tel)
}

func (a *StateDBActivities) EndRun(ctx context.Context, input EndRunInput) error {
	run := &state.Run{Pool: a.statePool, ID: input.RunID, Logger: a.tel.Logger}
	return run.End(ctx, input.Passed)
}

// SaveDowntimeResults writes one row per disruption.
func (a *StateDBActivities) SaveDowntimeResults(ctx context.Context, input SaveDowntimeInput) error {
	rows, unmatched, err := downtimeRows(input)
	if err != nil {
		return err
	}
	if a.tel != nil {
		if missing := len(input.DisruptedAt) - len(rows); missing > 0 {
			a.tel.Logger.Warn("disruptions with no outage", slog.Int("count", missing))
		}
		for _, o := range unmatched {
			a.tel.Logger.Warn("outage matched no disruption",
				slog.Time("first_failure", o.FirstFailure),
				slog.Float64("down_ms", o.DownMs),
			)
		}
	}
	return state.SaveDowntimeResults(ctx, a.statePool, input.RunID, rows, a.tel)
}

// downtimeRows makes one row per disruption from the first long outage after it.
// It also returns the other long outages.
func downtimeRows(input SaveDowntimeInput) ([]state.DowntimeRow, []probe.Outage, error) {
	readable := longOutages(input.Result.Readable.Outages)

	var rows []state.DowntimeRow
	var skippedOutages []probe.Outage

	if len(input.DisruptedAt) == 0 {
		return nil, nil, fmt.Errorf("no disruption timestamps recorded")
	}

	disruptedAtIndex := 0
	disruptionAt := input.DisruptedAt[disruptedAtIndex]
	writable := longOutages(input.Result.Writable.Outages)
	outageIndex := 0
	for outageIndex < len(writable) && disruptedAtIndex < len(input.DisruptedAt) {
		//outage happens before a certain disruption window, incliuding extra outages happens after the previous failover
		for outageIndex < len(writable) && writable[outageIndex].FirstFailure.Before(disruptionAt) {
			skippedOutages = append(skippedOutages, writable[outageIndex])
			outageIndex++
		}

		if outageIndex == len(writable) {
			break
		}
		outage := writable[outageIndex]

		//Skips disruption that has no outages (rare edge case)
		for disruptedAtIndex+1 < len(input.DisruptedAt) && !outage.FirstFailure.Before(input.DisruptedAt[disruptedAtIndex+1]) {
			disruptedAtIndex++
		}

		disruptionAt = input.DisruptedAt[disruptedAtIndex]

		if outage.FirstOKAfter.IsZero() {
			return nil, nil, fmt.Errorf("disruption %d never recovered before the probe stopped", disruptedAtIndex+1)
		}
		//outage during a failover
		row := state.DowntimeRow{
			Provider:           string(input.Provider),
			Disruption:         string(input.Disruption),
			Repetition:         disruptedAtIndex + 1,
			DisruptedAt:        disruptionAt,
			UsableAgainAt:      outage.FirstOKAfter,
			SyncEndedAt:        outage.FirstOKAfter,
			WritableDowntimeMs: outage.DownMs,
			FullRecoveryMs:     outage.DownMs,
			LostCommits:        outage.LostCommits,
			ProbeIntervalMs:    input.Result.IntervalMs,
			ProbeFailures:      outage.Failures,
			ProbeErrors:        outage.Errors,
		}
		// reads downtime states zero when reads are never broken
		if read, ok := matchReadable(readable, outage); ok {
			row.ReadableDowntimeMs = read.DownMs
		}

		rows = append(rows, row)
		disruptedAtIndex++
		outageIndex++
		if disruptedAtIndex < len(input.DisruptedAt) {
			disruptionAt = input.DisruptedAt[disruptedAtIndex]
		}
	}

	// Outages happens after the last disruption's window
	skippedOutages = append(skippedOutages, writable[outageIndex:]...)

	return rows, skippedOutages, nil
}

// longOutages keeps the outages that last at least a second, lost commits, orex
// never ended.
func longOutages(outages []probe.Outage) []probe.Outage {
	var kept []probe.Outage
	for _, o := range outages {
		if o.DownMs >= 1000 || o.LostCommits > 0 || o.FirstOKAfter.IsZero() {
			kept = append(kept, o)
		}
	}
	return kept
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
