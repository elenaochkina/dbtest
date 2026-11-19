// Package harness launches short-lived containers and reads back what they printed.
package harness

import (
	"bytes"
	"context"
)

type Spec struct {
	Image string
	Args  []string // appended to the image's entrypoint
	Name  string
	// Key tags the launch so Find can locate it after a lost Run result. It is
	// the same on every retry of one activity.
	Key string
	// SelfExits marks a container that exits on its own; Stop waits for it.
	SelfExits bool
}

// Handle is the runner's identifier for a started container. It carries
// everything Stop needs: Run and Stop happen in separate activities, and nothing
// else travels between them.
type Handle struct {
	ID        string
	SelfExits bool
}

// Runner starts a container somewhere and collects what it printed.
type Runner interface {
	// Run creates and starts the container and returns once it is running.
	Run(ctx context.Context, spec Spec) (Handle, error)

	// Stop ends the container and returns its output, stdout and stderr mixed.
	// The output is returned even with an error.
	//
	// A second call returns the same output on Cloud Run and Fargate, and none
	// on Docker.
	Stop(ctx context.Context, h Handle) ([]byte, error)

	// Find returns the container launched with spec.Key, running or exited, and
	// whether there is one.
	Find(ctx context.Context, spec Spec) (Handle, bool, error)

	// Exited reports whether the container has ended. A container the runner no
	// longer knows has ended.
	Exited(ctx context.Context, h Handle) (bool, error)
}

// Images names the probe, bench and tester containers: an image tag on
// Docker, a task definition on Fargate, a job on Cloud Run.
type Images struct {
	Probe  string
	Bench  string
	Tester string
}

// Definitions that terraform/gcp and harnesterraform/aws create.
const (
	ProbeName  = "dbtest-probe"
	BenchName  = "dbtest-bench"
	TesterName = "dbtest-tester"
)

// RunnerName names a Runner implementation. A cloud lists the ones it can build.
type RunnerName string

const (
	Docker   RunnerName = "docker"
	Fargate  RunnerName = "fargate"
	CloudRun RunnerName = "cloudrun"
)

// Tail returns the last few lines of container output, for an error message.
func Tail(out []byte) string {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return string(bytes.Join(lines, []byte("\n")))
}
