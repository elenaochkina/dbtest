package cloud

import (
	"context"
	"fmt"
	"time"

	dockerclient "github.com/docker/docker/client"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
)

// Local is the machine dbtest runs on, reached through the Docker daemon.
type Local struct {
	client *dockerclient.Client
}

// NewLocal builds a Docker client from the environment.
func NewLocal() (Cloud, error) {
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Local{client: client}, nil
}

func (c *Local) Name() provider.CloudName { return provider.Local }

// Check reports whether the Docker daemon answers.
func (c *Local) Check(ctx context.Context) provider.Status {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := c.client.Ping(ctx); err != nil {
		return provider.Status{State: provider.StateMisconfigured, Detail: "docker is not running"}
	}
	return provider.Status{State: provider.StateReady, Detail: "docker is running"}
}

func (c *Local) Runner() harness.RunnerName { return harness.Docker }

func (c *Local) Supports(service provider.DBServiceName) bool {
	return Supports(provider.Local, service)
}

// Client returns the Docker client the cloud's services use.
func (c *Local) Client() *dockerclient.Client { return c.client }

func init() {
	Register(provider.Local, NewLocal)
}

var _ Cloud = (*Local)(nil)
