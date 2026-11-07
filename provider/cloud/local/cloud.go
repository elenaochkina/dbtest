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

// Cloud is the machine dbtest runs on, reached through the Docker daemon.
type Cloud struct {
	client *dockerclient.Client
}

// New builds a Docker client from the environment.
func New() (cloud.Cloud, error) {
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Cloud{client: client}, nil
}

func (c *Cloud) Name() provider.CloudName { return provider.Local }

// Check reports whether the Docker daemon answers.
func (c *Cloud) Check(ctx context.Context) provider.Status {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := c.client.Ping(ctx); err != nil {
		return provider.Status{State: provider.StateMisconfigured, Detail: "docker is not running"}
	}
	return provider.Status{State: provider.StateReady, Detail: "docker is running"}
}

func (c *Cloud) Runners() []harness.RunnerName { return []harness.RunnerName{harness.Docker} }

// NewRunner builds the Docker runner on the cloud's client.
func (c *Cloud) NewRunner(name harness.RunnerName, tel *telemetry.Telemetry) (harness.Runner, error) {
	if name != harness.Docker {
		return nil, cloud.UnknownRunner(c, name)
	}
	return newDockerRunner(c.client, tel), nil
}

func (c *Cloud) Supports(service provider.DBServiceName) bool {
	return cloud.Supports(provider.Local, service)
}

// Client returns the Docker client the cloud's services use.
func (c *Cloud) Client() *dockerclient.Client { return c.client }

func init() {
	cloud.Register(provider.Local, New)
}

var _ cloud.Cloud = (*Cloud)(nil)
