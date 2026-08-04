package temporal

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/probe"
	"github.com/elenaochkina/dbtest/telemetry"
	"go.temporal.io/sdk/activity"
)

// ProbeInput launches the prober. Name is derived from the run, so a retried
// Start adopts the container a previous attempt created.
type ProbeInput struct {
	Runner      harness.RunnerName
	Image       string
	Network     string
	Name        string
	DSN         string
	Interval    time.Duration
	Repetitions int
	MaxDuration time.Duration
}

// ProbeActivities run the prober in a container and read back what it measured.
type ProbeActivities struct {
	tel *telemetry.Telemetry
}

func NewProbeActivities(tel *telemetry.Telemetry) *ProbeActivities {
	return &ProbeActivities{tel: tel}
}

// StartProbe starts the prober detached, before anything can break.
func (a *ProbeActivities) StartProbe(ctx context.Context, input ProbeInput) (harness.Handle, error) {
	r, err := harness.Run(input.Runner, a.tel)
	if err != nil {
		return harness.Handle{}, fmt.Errorf("harness %q: %w", input.Runner, err)
	}
	h, err := r.Start(ctx, probeSpec(input))
	if err != nil {
		return harness.Handle{}, fmt.Errorf("start probe: %w", err)
	}
	return h, nil
}

// CollectProbe waits for the prober to finish and reads its result. It does not
// stop the container: reading logs is repeatable, removing them is not, so
// StopProbe owns teardown and this stays safe to retry.
func (a *ProbeActivities) CollectProbe(ctx context.Context, input ContainerInput) (probe.Result, error) {
	r, err := harness.Run(input.Runner, a.tel)
	if err != nil {
		return probe.Result{}, fmt.Errorf("harness %q: %w", input.Runner, err)
	}

	stop := heartbeat(ctx)
	defer stop()

	code, err := r.Wait(ctx, input.Handle)
	if err != nil {
		return probe.Result{}, fmt.Errorf("wait probe: %w", err)
	}

	out, err := r.Output(ctx, input.Handle)
	if err != nil {
		return probe.Result{}, fmt.Errorf("probe output: %w", err)
	}

	res, err := probe.Parse(out)
	if err != nil {
		logs, _ := r.Logs(ctx, input.Handle)
		return probe.Result{}, fmt.Errorf("%w: %s", err, tailLogs(logs))
	}

	// The prober exits non-zero when the measurement is incomplete but prints its
	// result either way. res.Error carries that verdict; the caller decides.
	if a.tel != nil {
		a.tel.Logger.Info("collected probe result",
			slog.Int("exit_code", code),
			slog.Int("samples", res.Samples),
			slog.Float64("writable_down_ms", res.Writable.LongestDownMs),
			slog.Int64("lost_commits", res.LostCommits),
		)
	}
	return res, nil
}

// StopProbe stops and removes the prober. Call it after CollectProbe — Stop
// removes the container CollectProbe reads from.
func (a *ProbeActivities) StopProbe(ctx context.Context, input ContainerInput) error {
	r, err := harness.Run(input.Runner, a.tel)
	if err != nil {
		return fmt.Errorf("harness %q: %w", input.Runner, err)
	}
	return r.Stop(ctx, input.Handle)
}

func probeSpec(in ProbeInput) harness.Spec {
	return harness.Spec{
		Image:   in.Image,
		Name:    in.Name,
		Network: in.Network,
		Args: []string{
			"-dsn", in.DSN,
			"-interval", in.Interval.String(),
			"-repetitions", strconv.Itoa(in.Repetitions),
			"-max-duration", in.MaxDuration.String(),
		},
	}
}

// heartbeat pings Temporal while an activity blocks, so a dead worker is noticed
// before StartToCloseTimeout elapses.
func heartbeat(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				activity.RecordHeartbeat(ctx)
			}
		}
	}()
	return func() { close(done) }
}
