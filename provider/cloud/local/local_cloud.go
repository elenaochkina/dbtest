// Package local is the machine dbtest runs on, reached through Docker.
package local

import (
	"context"
	"fmt"
	"time"

	dockerclient "github.com/docker/docker/client"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/telemetry"
)

// LocalCloud is the machine dbtest runs on, reached through the Docker daemon.
type LocalCloud struct {
	client *dockerclient.Client
}

// New builds a Docker client from the environment.
func New() (cloud.Cloud, error) {
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &LocalCloud{client: client}, nil
}

func (c *LocalCloud) Name() provider.CloudName { return provider.Local }

// Check reports whether the Docker daemon answers.
func (c *LocalCloud) Check(ctx context.Context) provider.Status {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := c.client.Ping(ctx); err != nil {
		return provider.Status{State: provider.StateMisconfigured, Detail: "docker is not running"}
	}
	return provider.Status{State: provider.StateReady, Detail: "docker is running"}
}

func (c *LocalCloud) Runners() []harness.RunnerName { return []harness.RunnerName{harness.Docker} }

// NewRunner builds the Docker runner on the cloud's client.
func (c *LocalCloud) NewRunner(name harness.RunnerName, tel *telemetry.Telemetry) (harness.Runner, error) {
	if name != harness.Docker {
		return nil, cloud.UnknownRunner(c, name)
	}
	return newDockerRunner(c.client, tel), nil
}

func (c *LocalCloud) Images(harness.RunnerName) harness.Images { return DockerImages() }

// DockerImages returns the probe and bench image tags from .env, defaulting to
// the tags make images builds.
func DockerImages() harness.Images {
	return harness.Images{
		Probe: cloud.EnvOr("DBTEST_PROBE_IMAGE", "dbtest/probe:dev"),
		Bench: cloud.EnvOr("DBTEST_BENCH_IMAGE", "dbtest/bench:dev"),
	}
}

func (c *LocalCloud) Supports(service provider.DBServiceName) bool {
	return cloud.Supports(provider.Local, service)
}

// Client returns the Docker client the cloud's services use.
func (c *LocalCloud) Client() *dockerclient.Client { return c.client }

func init() {
	cloud.Register(provider.Local, New)
}

var _ cloud.Cloud = (*LocalCloud)(nil)
