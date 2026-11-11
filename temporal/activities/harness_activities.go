package activities

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/probe"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/telemetry"
	"go.temporal.io/sdk/temporal"
)

// RunnerInput names the cloud whose runner starts the run's containers.
type RunnerInput struct {
	Cloud provider.CloudName
}

// ProbeInput describes the prober container to start.
type ProbeInput struct {
	Cloud  provider.CloudName
	Runner harness.RunnerName // which of the cloud's runners starts it
	Image  string
	Name   string
	// DSN addresses the database as a sibling container sees it, which is not the
	// address the worker uses.
	DSN          string
	Interval     time.Duration
	Timeout      time.Duration
	WriteTimeout time.Duration
	MaxDuration  time.Duration
}

// ProbeReadyInput describes the check that the measurement probe is writing:
// the probe image in sampling mode against the cluster's internal address.
type ProbeReadyInput struct {
	Cloud  provider.CloudName
	Runner harness.RunnerName
	Image  string
	Name   string
	DSN    string
}

// BenchContainerInput describes the bench container to run.
type BenchContainerInput struct {
	Cloud  provider.CloudName
	Runner harness.RunnerName
	Image  string
	Name   string
	// DSN addresses the database as a sibling container sees it, which is not the
	// address the worker uses.
	DSN      string
	Workload string // pgbench or warehouse
	// Scale sets how much data pgbench writes.
	Scale int
}

// ContainerInput is the input to a Stop activity.
// The workflow builds it from the handle Run returned.
type ContainerInput struct {
	Cloud  provider.CloudName
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

// ResolvedRunner is the runner that starts a run's containers and the images it starts.
type ResolvedRunner struct {
	Name   harness.RunnerName
	Images harness.Images
}

// ResolveRunner builds the cloud and returns its default runner and that runner's images.
func (a *HarnessActivities) ResolveRunner(ctx context.Context, input RunnerInput) (ResolvedRunner, error) {
	c, err := cloud.NewCloudProvider(input.Cloud)
	if err != nil {
		return ResolvedRunner{}, temporal.NewNonRetryableApplicationError(err.Error(), "UnknownCloud", err)
	}
	name := c.Runners()[0]
	return ResolvedRunner{Name: name, Images: c.Images(name)}, nil
}

// runner builds the named runner in the named cloud.
func (a *HarnessActivities) runner(cloudName provider.CloudName, name harness.RunnerName) (harness.Runner, error) {
	c, err := cloud.NewCloudProvider(cloudName)
	if err != nil {
		return nil, fmt.Errorf("cloud %q: %w", cloudName, err)
	}
	r, err := c.NewRunner(name, a.tel)
	if err != nil {
		return nil, fmt.Errorf("runner %q: %w", name, err)
	}
	return r, nil
}

// StartProbe launches the prober and returns once it is running.
// The prober polls until it is stopped, so the workflow holds the handle across
// the disruptions.
func (a *HarnessActivities) StartProbe(ctx context.Context, input ProbeInput) (harness.Handle, error) {
	r, err := a.runner(input.Cloud, input.Runner)
	if err != nil {
		return harness.Handle{}, err
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
	r, err := a.runner(input.Cloud, input.Runner)
	if err != nil {
		return probe.Result{}, err
	}

	// The prober prints its result before exiting, so parse before judging the error.
	out, stopErr := r.Stop(ctx, input.Handle)
	res, parseErr := probe.Parse(out)
	if parseErr != nil {
		if stopErr != nil {
			return probe.Result{}, fmt.Errorf("stop probe: %w", stopErr)
		}
		return probe.Result{}, fmt.Errorf("%w: %s", parseErr, harness.Tail(out))
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
	r, err := a.runner(input.Cloud, input.Runner)
	if err != nil {
		return err
	}

	h, err := r.Run(ctx, benchSpec(input))
	if err != nil {
		return fmt.Errorf("start seed: %w", err)
	}
	// Stops the container again when the stop below fails.
	stopped := false
	defer func() {
		if !stopped {
			_, _ = r.Stop(context.WithoutCancel(ctx), h)
		}
	}()

	// SelfExits, so this waits for the seed rather than cutting it off.
	out, err := r.Stop(ctx, h)
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	stopped = true

	if a.tel != nil {
		a.tel.Logger.Info("database seeded",
			slog.String("image", input.Image),
			slog.Int("scale", input.Scale),
			slog.String("output", harness.Tail(out)),
		)
	}
	return nil
}

// CheckProbeReadiness runs the probe in sampling mode on the cloud's runner and
// reports whether the measurement probe's counter rose above 0 within 30 seconds.
func (a *HarnessActivities) CheckProbeReadiness(ctx context.Context, input ProbeReadyInput) (bool, error) {
	r, err := a.runner(input.Cloud, input.Runner)
	if err != nil {
		return false, err
	}
	h, err := r.Run(ctx, harness.Spec{
		Image:     input.Image,
		Name:      input.Name,
		Args:      []string{"-dsn", input.DSN, "-sampling", "30s"},
		SelfExits: true,
	})
	if err != nil {
		return false, fmt.Errorf("start sampling check: %w", err)
	}
	out, err := r.Stop(ctx, h)
	if err != nil {
		if a.tel != nil {
			a.tel.Logger.Warn("probe is not sampling", slog.Any("error", err), slog.String("output", harness.Tail(out)))
		}
		return false, nil
	}
	if a.tel != nil {
		a.tel.Logger.Info("probe is sampling", slog.String("name", input.Name))
	}
	return true, nil
}

func probeSpec(in ProbeInput) harness.Spec {
	args := []string{"-dsn", in.DSN}
	if in.Interval > 0 {
		args = append(args, "-interval", in.Interval.String())
	}
	if in.Timeout > 0 {
		args = append(args, "-timeout", in.Timeout.String())
	}
	if in.WriteTimeout > 0 {
		args = append(args, "-write-timeout", in.WriteTimeout.String())
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
