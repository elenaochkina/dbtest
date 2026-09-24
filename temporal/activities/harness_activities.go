package activities

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/probe"
	"github.com/elenaochkina/dbtest/telemetry"
)

// ProbeInput describes the prober container to start.
type ProbeInput struct {
	Runner harness.RunnerName // which backend runs it
	Image  string
	Name   string
	// DSN addresses the database as a sibling container sees it, which is not the
	// address the worker uses.
	DSN         string
	Interval    time.Duration
	MaxDuration time.Duration
}

// BenchContainerInput describes the bench container to run.
type BenchContainerInput struct {
	Runner harness.RunnerName
	Image  string
	Name   string
	// DSN addresses the database as a sibling container sees it, which is not the
	// address the worker uses.
	DSN      string
	Workload string // pgbench or warehouse
	// Scale sets how much data pgbench writes. It has to match across every run
	// being compared.
	Scale int
}

// ContainerInput is the input to a Stop activity.
// The workflow builds it from the handle Run returned.
type ContainerInput struct {
	Runner harness.RunnerName
	Handle harness.Handle
}

// HarnessActivities run the containers a measurement needs: the prober that
// watches the database, and bench that fills it.
type HarnessActivities struct {
	tel *telemetry.Telemetry
}

func NewHarnessActivities(tel *telemetry.Telemetry) *HarnessActivities {
	return &HarnessActivities{tel: tel}
}

// StartProbe launches the prober and returns once it is running.
// The prober polls until it is stopped, so the workflow holds the handle across
// the disruptions.
func (a *HarnessActivities) StartProbe(ctx context.Context, input ProbeInput) (harness.Handle, error) {
	r, err := harness.New(input.Runner, a.tel)
	if err != nil {
		return harness.Handle{}, fmt.Errorf("runner %q: %w", input.Runner, err)
	}

	h, err := r.Run(ctx, probeSpec(input))
	if err != nil {
		return harness.Handle{}, fmt.Errorf("start probe: %w", err)
	}
	if a.tel != nil {
		a.tel.Logger.Info("probe started",
			slog.String("container_id", h.ID),
			slog.String("image", input.Image),
		)
	}
	return h, nil
}

// StopProbe ends the prober and returns what it measured.
//
// Not safe to retry: stopping discards the container.
func (a *HarnessActivities) StopProbe(ctx context.Context, input ContainerInput) (probe.Result, error) {
	r, err := harness.New(input.Runner, a.tel)
	if err != nil {
		return probe.Result{}, fmt.Errorf("runner %q: %w", input.Runner, err)
	}

	// The prober prints its result before exiting, so parse before judging the error.
	out, stopErr := r.Stop(ctx, input.Handle)
	res, parseErr := probe.Parse(out)
	if parseErr != nil {
		if stopErr != nil {
			return probe.Result{}, fmt.Errorf("stop probe: %w", stopErr)
		}
		return probe.Result{}, fmt.Errorf("%w: %s", parseErr, tailLines(out))
	}
	if stopErr != nil && a.tel != nil {
		a.tel.Logger.Warn("prober exited badly but produced a result",
			slog.Any("error", stopErr),
		)
	}

	if a.tel != nil {
		a.tel.Logger.Info("probe collected",
			slog.Int("samples", res.Samples),
			slog.Int("writable_outages", len(res.Writable.Outages)),
			slog.Float64("longest_writable_down_ms", res.Writable.LongestDownMs),
			slog.Int64("lost_commits", res.LostCommits),
		)
	}
	return res, nil
}

// InitializeBenchContainer runs bench's setup phase and waits for it to finish.
// The output is discarded: a failed seed arrives as a non-zero exit.
func (a *HarnessActivities) InitializeBenchContainer(ctx context.Context, input BenchContainerInput) error {
	r, err := harness.New(input.Runner, a.tel)
	if err != nil {
		return fmt.Errorf("runner %q: %w", input.Runner, err)
	}

	h, err := r.Run(ctx, benchSpec(input))
	if err != nil {
		return fmt.Errorf("start seed: %w", err)
	}
	// Cleanup for the paths that never reach the stop below. Stopping a container
	// that is already gone fails; the error is discarded.

	defer func() { _, _ = r.Stop(context.WithoutCancel(ctx), h) }()

	// SelfExits, so this waits for the seed rather than cutting it off.
	out, err := r.Stop(ctx, h)
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	if a.tel != nil {
		a.tel.Logger.Info("database seeded",
			slog.String("image", input.Image),
			slog.Int("scale", input.Scale),
			slog.String("output", tailLines(out)),
		)
	}
	return nil
}

func probeSpec(in ProbeInput) harness.Spec {
	args := []string{"-dsn", in.DSN}
	if in.Interval > 0 {
		args = append(args, "-interval", in.Interval.String())
	}
	if in.MaxDuration > 0 {
		args = append(args, "-max-duration", in.MaxDuration.String())
	}
	return harness.Spec{
		Image: in.Image,
		Name:  in.Name,
		Args:  args,
		// The prober runs until Stop terminates it.
		SelfExits: false,
	}
}

func benchSpec(in BenchContainerInput) harness.Spec {
	args := []string{"-dsn", in.DSN, "-init"}
	if in.Workload != "" {
		args = append(args, "-workload", in.Workload)
	}
	if in.Scale > 0 {
		args = append(args, "-scale", strconv.Itoa(in.Scale))
	}
	return harness.Spec{
		Image: in.Image,
		Name:  in.Name,
		Args:  args,
		// Seeding ends on its own, so Stop waits for it.
		SelfExits: true,
	}
}

// tailLines returns the last few lines of container output, for an error message.
func tailLines(out []byte) string {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return string(bytes.Join(lines, []byte("\n")))
}
