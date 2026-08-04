package activities

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/elenaochkina/dbtest/workload"
)

// BenchInput launches the load generator. Name is derived from the run, so a
// retried Start adopts the container a previous attempt created.
type BenchInput struct {
	Runner   harness.RunnerName
	Image    string
	Network  string
	Name     string
	DSN      string
	Workload workload.WorkloadName
	Config   workload.Config
	Init     bool // run the setup phase and exit
}

// PgBenchActivities run the load generator in a container.
type PgBenchActivities struct {
	tel *telemetry.Telemetry
}

func NewPgBenchActivities(tel *telemetry.Telemetry) *PgBenchActivities {
	return &PgBenchActivities{tel: tel}
}

// RunBenchInit runs the workload's setup phase to completion. pgbench -i drops
// and recreates its tables, so a retry is harmless.
func (a *PgBenchActivities) RunBenchInit(ctx context.Context, input BenchInput) error {
	r, err := harness.Run(input.Runner, a.tel)
	if err != nil {
		return fmt.Errorf("harness %q: %w", input.Runner, err)
	}

	input.Init = true
	h, err := r.Start(ctx, benchSpec(input))
	if err != nil {
		return fmt.Errorf("start bench init: %w", err)
	}

	code, err := r.Wait(ctx, h)
	if err != nil {
		return fmt.Errorf("wait bench init: %w", err)
	}
	if code != 0 {
		logs, _ := r.Logs(ctx, h)
		return fmt.Errorf("bench init exited %d: %s", code, tailLogs(logs))
	}
	return r.Stop(ctx, h)
}

// StartBench starts the load generator detached. It dies at the disruption and
// is never waited on.
func (a *PgBenchActivities) StartBench(ctx context.Context, input BenchInput) (harness.Handle, error) {
	r, err := harness.Run(input.Runner, a.tel)
	if err != nil {
		return harness.Handle{}, fmt.Errorf("harness %q: %w", input.Runner, err)
	}
	input.Init = false
	h, err := r.Start(ctx, benchSpec(input))
	if err != nil {
		return harness.Handle{}, fmt.Errorf("start bench: %w", err)
	}
	return h, nil
}

// StopBench stops and removes the load generator. A missing container is already
// treated as success, so this is safe to retry and safe to call twice.
func (a *PgBenchActivities) StopBench(ctx context.Context, input ContainerInput) error {
	r, err := harness.Run(input.Runner, a.tel)
	if err != nil {
		return fmt.Errorf("harness %q: %w", input.Runner, err)
	}
	return r.Stop(ctx, input.Handle)
}

func benchSpec(in BenchInput) harness.Spec {
	args := []string{
		"-workload", string(in.Workload),
		"-dsn", in.DSN,
		"-scale", strconv.Itoa(in.Config.ScaleFactor),
		"-clients", strconv.Itoa(in.Config.Clients),
		"-duration", in.Config.Duration.String(),
		"-seed", strconv.FormatInt(in.Config.Seed, 10),
		"-warehouses", strconv.Itoa(in.Config.Warehouses),
	}
	if in.Init {
		args = append(args, "-init")
	}
	return harness.Spec{Image: in.Image, Name: in.Name, Network: in.Network, Args: args}
}

// tailLogs trims a container's stderr to something an error message can carry.
func tailLogs(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 500 {
		return "..." + s[len(s)-500:]
	}
	return s
}
