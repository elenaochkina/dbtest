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
	if pre := beforeFirst(input.Result.Writable.Outages, input.DisruptedAt); len(pre) > 0 && a.tel != nil {
		var ms float64
		for _, o := range pre {
			ms += o.DownMs
		}
		a.tel.Logger.Warn("outages before the first disruption",
			slog.Int("count", len(pre)),
			slog.Float64("total_down_ms", ms),
			slog.Time("first", pre[0].FirstFailure),
		)
	}
	rows, err := downtimeRows(input)
	if err != nil {
		return err
	}
	return state.SaveDowntimeResults(ctx, a.statePool, input.RunID, rows, a.tel)
}

// downtimeRows pairs the two levels the prober records. Writable is the
// authority: it is the stronger condition, so every outage appears in it.
// If there is no readable outage, it is marked as zero.
// Outages are grouped by the disruption they followed, one row per disruption.
func downtimeRows(input SaveDowntimeInput) ([]state.DowntimeRow, error) {
	readable := input.Result.Readable.Outages

	windows, err := groupByDisruption(input.Result.Writable.Outages, input.DisruptedAt)
	if err != nil {
		return nil, err
	}
	rows := make([]state.DowntimeRow, 0, len(windows))

	for i, outages := range windows {
		takeover := outages[0]
		last := outages[len(outages)-1]
		row := state.DowntimeRow{
			Provider:           string(input.Provider),
			Disruption:         string(input.Disruption),
			Repetition:         i + 1,
			DisruptedAt:        input.DisruptedAt[i],
			UsableAgainAt:      takeover.FirstOKAfter,
			SyncEndedAt:        last.FirstOKAfter,
			WritableDowntimeMs: takeover.DownMs,
			// Stays zero when no readable outage overlapse: reads never broke
			ReadableDowntimeMs: 0,
			FullRecoveryMs:     msBetween(takeover.LastOK, last.FirstOKAfter),
			ProbeIntervalMs:    input.Result.IntervalMs,
			ProbeErrors:        map[string]int{},
		}
		// Everything after the takeover belongs to the same disruption, so the
		// totals cover the whole window.
		for _, o := range outages {
			row.LostCommits += o.LostCommits
			row.ProbeFailures += o.Failures
			for k, v := range o.Errors {
				row.ProbeErrors[k] += v
			}
		}
		if r, ok := matchReadable(readable, takeover); ok {
			row.ReadableDowntimeMs = r.DownMs
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func msBetween(from, to time.Time) float64 {
	return float64(to.Sub(from).Microseconds()) / 1000
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

// groupByDisruption puts each outage in the window of the disruption it followed.
// The first outage in a window is the disruption's process;
// the rest are writer blips during sync stage.
func groupByDisruption(outages []probe.Outage, disruptedAt []time.Time) ([][]probe.Outage, error) {
	if len(disruptedAt) == 0 {
		return nil, fmt.Errorf("no disruption timestamps recorded")
	}

	windows := make([][]probe.Outage, len(disruptedAt))
	for _, o := range outages {
		i := windowOf(o.FirstFailure, disruptedAt)
		// A cluster interrupts itself while it is still settling, so an outage
		// before the first disruption belongs to no window.
		if i < 0 {
			continue
		}
		windows[i] = append(windows[i], o)
	}

	for i, w := range windows {
		if len(w) == 0 {
			return nil, fmt.Errorf("disruption %d at %s produced no outage", i+1, disruptedAt[i])
		}
		if w[len(w)-1].FirstOKAfter.IsZero() {
			return nil, fmt.Errorf("disruption %d never recovered before the probe stopped", i+1)
		}
	}
	return windows, nil
}

// beforeFirst returns the outages that began before the first disruption.
func beforeFirst(outages []probe.Outage, disruptedAt []time.Time) []probe.Outage {
	if len(disruptedAt) == 0 {
		return nil
	}
	var pre []probe.Outage
	for _, o := range outages {
		if o.FirstFailure.Before(disruptedAt[0]) {
			pre = append(pre, o)
		}
	}
	return pre
}

// windowOf returns the index of the last disruption at or before at, or -1.
func windowOf(at time.Time, disruptedAt []time.Time) int {
	idx := -1
	for i, d := range disruptedAt {
		if at.Before(d) {
			break
		}
		idx = i
	}
	return idx
}
