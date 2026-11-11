// Package harness launches short-lived containers and reads back what they
// printed.
package harness

import (
	"bytes"
	"context"
)

type Spec struct {
	Image string
	Args  []string // appended to the image's entrypoint
	Name  string
	// SelfExits marks a container that ends on its own, the way bench does when
	// its run finishes. Stop waits for those rather than terminating them.
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

	// Stop ends the container and returns everything it printed, stdout and
	// stderr together. Fargate cannot separate the two, so nothing may depend on
	// the split. Output is returned even when the error is non-nil, because a
	// container that exits non-zero has usually still printed its result.
	//
	// Calling it twice is not an error, but only the first call returns output:
	// the second finds the container already gone and discard an error.
	Stop(ctx context.Context, h Handle) ([]byte, error)
}

// Images names what a runner starts for the probe and bench containers: image
// tags for Docker, task definitions for Fargate, jobs for Cloud Run.
type Images struct {
	Probe string
	Bench string
}

// ProbeName and BenchName name the probe and bench Cloud Run jobs and ECS task
// definitions that terraform/gcp and terraform/aws create.
const (
	ProbeName = "dbtest-probe"
	BenchName = "dbtest-bench"
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
