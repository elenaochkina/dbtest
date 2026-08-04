package docker

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/telemetry"
)

type dockerRunner struct {
	client *dockerclient.Client
	tel    *telemetry.Telemetry
}

// New creates a Docker runner.
func New(tel *telemetry.Telemetry) (*dockerRunner, error) {
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &dockerRunner{client: client, tel: tel}, nil
}

// Start creates and starts the container.
func (r *dockerRunner) Start(ctx context.Context, spec harness.Spec) (harness.Handle, error) {
	var netConfig *network.NetworkingConfig
	if spec.Network != "" {
		netConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{spec.Network: {}},
		}
	}

	resp, err := r.client.ContainerCreate(ctx,
		&container.Config{
			Image: spec.Image,
			Cmd:   spec.Args,
			Tty:   false, // keeps stdout and stderr separately framed
		},
		&container.HostConfig{},
		netConfig, nil, spec.Name,
	)
	id := resp.ID
	if err != nil {
		if errdefs.IsNotFound(err) {
			return harness.Handle{}, fmt.Errorf("image %q not found locally — run `make images`: %w", spec.Image, err)
		}
		//Docker adoption: try to find a same name container
		if spec.Name == "" || !errdefs.IsConflict(err) {
			return harness.Handle{}, fmt.Errorf("container create %q: %w", spec.Name, err)
		}
		existing, ierr := r.client.ContainerInspect(ctx, spec.Name)
		if ierr != nil {
			return harness.Handle{}, fmt.Errorf("adopt existing container %q: %w", spec.Name, ierr)
		}
		id = existing.ID
		if r.tel != nil {
			r.tel.Logger.Info("adopted container from a previous attempt",
				slog.String("container_id", id),
				slog.String("name", spec.Name),
			)
		}
	}

	// Starting an already-running container is a no-op
	if err := r.client.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return harness.Handle{}, fmt.Errorf("container start: %w", err)
	}

	if r.tel != nil {
		r.tel.Logger.Info("started harness container",
			slog.String("container_id", id),
			slog.String("name", spec.Name),
			slog.String("image", spec.Image),
			slog.String("network", spec.Network),
		)
	}
	return harness.Handle{ID: id}, nil
}

// Wait blocks until the container exits and reports its exit code.
func (r *dockerRunner) Wait(ctx context.Context, h harness.Handle) (int, error) {
	statusCh, errCh := r.client.ContainerWait(ctx, h.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return 0, fmt.Errorf("container wait: %w", err)
		}
		return 0, nil
	case status := <-statusCh:
		return int(status.StatusCode), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Stop sends SIGTERM and waits for the container to exit, leaving it in place.
// The grace period has to outlast one sample plus writing the result.
func (r *dockerRunner) Stop(ctx context.Context, h harness.Handle) error {
	timeout := 10
	if err := r.client.ContainerStop(ctx, h.ID, container.StopOptions{Timeout: &timeout}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("container stop: %w", err)
	}
	return nil
}

// Remove discards the container, and with it everything it printed.
func (r *dockerRunner) Remove(ctx context.Context, h harness.Handle) error {
	if err := r.client.ContainerRemove(ctx, h.ID, container.RemoveOptions{Force: true}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("container remove: %w", err)
	}
	return nil
}

func (r *dockerRunner) Output(ctx context.Context, h harness.Handle) ([]byte, error) {
	stdout, _, err := r.streams(ctx, h)
	return stdout, err
}

func (r *dockerRunner) Logs(ctx context.Context, h harness.Handle) ([]byte, error) {
	_, stderr, err := r.streams(ctx, h)
	return stderr, err
}

func (r *dockerRunner) streams(ctx context.Context, h harness.Handle) ([]byte, []byte, error) {
	rc, err := r.client.ContainerLogs(ctx, h.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("container logs: %w", err)
	}
	defer rc.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, rc); err != nil {
		return nil, nil, fmt.Errorf("demultiplex logs: %w", err)
	}
	return stdout.Bytes(), stderr.Bytes(), nil
}

func newRunner(tel *telemetry.Telemetry) (harness.Runner, error) {
	return New(tel)
}

func init() {
	harness.Register(harness.Docker, newRunner)
}

var _ harness.Runner = (*dockerRunner)(nil)
