package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
)

// temporalServer runs the Temporal dev server in a Docker container.
type temporalServer struct {
	docker  *dockerclient.Client
	name    string
	network string
	address string
	uiURL   string
}

func newTemporalServer(docker *dockerclient.Client, name, network string) *temporalServer {
	return &temporalServer{docker: docker, name: name, network: network}
}

// start uses the container if it is running, starts it if it is stopped, or
// creates it if it is missing, then waits until the server answers health
// checks. It returns the API address and the web UI URL on separate lines.
func (s *temporalServer) start(ctx context.Context) (string, error) {
	info, err := s.docker.ContainerInspect(ctx, s.name)
	if errdefs.IsNotFound(err) {
		if err := s.create(ctx); err != nil {
			return "", err
		}
		info, err = s.docker.ContainerInspect(ctx, s.name)
	}
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", s.name, err)
	}

	if !info.State.Running {
		if err := s.docker.ContainerStart(ctx, info.ID, container.StartOptions{}); err != nil {
			return "", fmt.Errorf("start %s: %w", s.name, err)
		}
		if info, err = s.docker.ContainerInspect(ctx, s.name); err != nil {
			return "", fmt.Errorf("inspect %s: %w", s.name, err)
		}
	}

	if err := joinNetwork(ctx, s.docker, s.network, s.name, info.NetworkSettings.Networks); err != nil {
		return "", err
	}

	apiBindings := info.NetworkSettings.Ports[nat.Port("7233/tcp")]
	uiBindings := info.NetworkSettings.Ports[nat.Port("8233/tcp")]
	if len(apiBindings) == 0 || len(uiBindings) == 0 {
		return "", fmt.Errorf("%s publishes no port for 7233 or 8233", s.name)
	}
	s.address = "localhost:" + apiBindings[0].HostPort
	s.uiURL = "http://localhost:" + uiBindings[0].HostPort

	if err := s.waitReady(ctx); err != nil {
		return "", err
	}
	return "API\t" + s.address + "\nUI\t" + s.uiURL, nil
}

// create creates a new container on free port: API preferably 7233, UI - 8233.
func (s *temporalServer) create(ctx context.Context) error {
	if _, err := s.docker.ImageInspect(ctx, "temporalio/temporal:1.8.2"); errdefs.IsNotFound(err) {
		reader, err := s.docker.ImagePull(ctx, "temporalio/temporal:1.8.2", image.PullOptions{})
		if err != nil {
			return fmt.Errorf("pull temporalio/temporal:1.8.2: %w", err)
		}
		io.Copy(io.Discard, reader)
		reader.Close()
	}

	apiPort, err := freePort(7233)
	if err != nil {
		return err
	}
	uiPort, err := freePort(8233)
	if err != nil {
		return err
	}
	if err := ensureNetwork(ctx, s.docker, s.network); err != nil {
		return err
	}
	_, err = s.docker.ContainerCreate(ctx,
		&container.Config{
			Image: "temporalio/temporal:1.8.2",
			Cmd: []string{"server", "start-dev", "--ip", "0.0.0.0", "--ui-ip", "0.0.0.0",
				"--db-filename", "/home/temporal/temporal.db"},
			ExposedPorts: nat.PortSet{"7233/tcp": {}, "8233/tcp": {}},
			Labels:       map[string]string{"dbtest.managed": "true", "dbtest.role": "temporal"},
		},
		&container.HostConfig{
			NetworkMode: container.NetworkMode(s.network),
			PortBindings: nat.PortMap{
				"7233/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(apiPort)}},
				"8233/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(uiPort)}},
			},
			Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: s.name, Target: "/home/temporal"}},
		},
		nil, nil, s.name)
	if err != nil {
		return fmt.Errorf("create %s: %w", s.name, err)
	}
	return nil
}

// waitReady checks every half second, for up to 30 seconds, until the server
// answers health checks.
func (s *temporalServer) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := s.check(ctx)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s not ready after 30s: %w", s.name, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// check connects to the server and asks for its health.
func (s *temporalServer) check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	temporalClient, err := client.DialContext(ctx, client.Options{
		HostPort: s.address,
		Logger:   log.NewStructuredLogger(slog.New(slog.DiscardHandler)),
	})
	if err != nil {
		return err
	}
	defer temporalClient.Close()
	_, err = temporalClient.CheckHealth(ctx, &client.CheckHealthRequest{})
	return err
}
