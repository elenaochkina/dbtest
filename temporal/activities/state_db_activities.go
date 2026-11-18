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
	Cloud    provider.CloudName
	Provider provider.DBServiceName
}

// SaveDowntimeInput persists what the prober measured, one row per disruption.
type SaveDowntimeInput struct {
	RunID      uuid.UUID
	Provider   provider.DBServiceName
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

// SaveTPCCInput persists one measured TPROC-C run.
type SaveTPCCInput struct {
	RunID            uuid.UUID
	Provider         provider.DBServiceName
	HighAvailability bool
	Repetition       int
	Bench            BenchContainerInput // warehouses, clients, ramp-up, duration
	Build            BenchResult         // the build's elapsed_s
	Run              BenchResult         // nopm, tpm, latencies
}

// SaveSearchInput persists a tester search: its trials and its result.
type SaveSearchInput struct {
	RunID            uuid.UUID
	Provider         provider.DBServiceName
	HighAvailability bool
	Search           BenchContainerInput // tool, size, ramp-up, duration, search bounds
	Build            BenchResult         // the build's elapsed_s
	Result           BenchResult         // the tester's trials and peak, saturation, decline
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
		Cloud:    string(input.Cloud),
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

// SaveTPCCResult writes one measured run.
func (a *StateDBActivities) SaveTPCCResult(ctx context.Context, input SaveTPCCInput) error {
	row := tpccRow(input.Bench, input.Build, input.Run.Metrics)
	row.Provider = string(input.Provider)
	row.HighAvailability = input.HighAvailability
	row.Repetition = input.Repetition
	row.VirtualUsers = input.Bench.Clients
	return state.SaveTPCCResult(ctx, a.statePool, input.RunID, row)
}

// SaveSearch writes each trial of a search as a search_trials row, in run
// order, and the search result as a searches row.
func (a *StateDBActivities) SaveSearch(ctx context.Context, input SaveSearchInput) error {
	throughput := map[string]string{"hammerdb": "nopm", "pgbench": "tps"}[input.Search.Tool]
	var trials []state.TrialRow
	measured := 0
	for _, trial := range input.Result.Trials {
		trials = append(trials, state.TrialRow{
			Clients:    trial.Clients,
			Repeat:     trial.Repeat,
			Throughput: trial.Metrics[throughput],
			Metrics:    trial.Metrics,
		})
		if !trial.Repeat {
			measured++
		}
	}
	metrics := input.Result.Metrics
	search := state.SearchRow{
		Tool:              input.Search.Tool,
		Provider:          string(input.Provider),
		HighAvailability:  input.HighAvailability,
		RampUpSeconds:     int(input.Search.RampUp.Seconds()),
		DurationSeconds:   int(input.Search.Duration.Seconds()),
		BuildSeconds:      input.Build.ElapsedS,
		MinClients:        input.Search.Search.MinClients,
		MaxClients:        input.Search.Search.MaxClients,
		Threshold:         input.Search.Search.Threshold,
		PeakClients:       int(metrics["peak_clients"]),
		PeakThroughput:    metrics["peak_"+throughput],
		SaturationClients: int(metrics["saturation_clients"]),
		Trials:            measured,
		Drift:             metrics["drift"],
	}
	if input.Search.Warehouses > 0 {
		search.Warehouses = &input.Search.Warehouses
	}
	if input.Search.Scale > 0 {
		search.Scale = &input.Search.Scale
	}
	if decline := int(metrics["decline_clients"]); decline > 0 {
		search.DeclineClients = &decline
	}
	return state.SaveSearch(ctx, a.statePool, input.RunID, search, trials)
}

// tpccRow fills the run settings and measurements shared by every TPROC-C row.
func tpccRow(bench BenchContainerInput, build BenchResult, metrics map[string]float64) state.TPCCRow {
	return state.TPCCRow{
		Warehouses:      bench.Warehouses,
		RampUpSeconds:   int(bench.RampUp.Seconds()),
		DurationSeconds: int(bench.Duration.Seconds()),
		BuildSeconds:    build.ElapsedS,
		NOPM:            metrics["nopm"],
		TPM:             metrics["tpm"],
		NewOrderP50Ms:   metrics["neword_p50_ms"],
		NewOrderP95Ms:   metrics["neword_p95_ms"],
		NewOrderP99Ms:   metrics["neword_p99_ms"],
		Select1P50Ms:    optionalMetric(metrics, "select1_p50_ms"),
		Select1P95Ms:    optionalMetric(metrics, "select1_p95_ms"),
		Metrics:         metrics,
	}
}

// optionalMetric returns the named metric, or nil when the run did not report it.
func optionalMetric(metrics map[string]float64, name string) *float64 {
	value, ok := metrics[name]
	if !ok {
		return nil
	}
	return &value
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
			WritableDowntimeMs: outage.DownMs,
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
