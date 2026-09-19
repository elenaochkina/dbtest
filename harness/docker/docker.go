package docker

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/telemetry"
)

type dockerRunner struct {
	client  *dockerclient.Client
	network string
	tel     *telemetry.Telemetry
}

// New creates a Docker runner. The network is runner-wide rather than per-spec,
// mirroring Fargate, where subnets and security groups are static infrastructure.
func New(tel *telemetry.Telemetry) (*dockerRunner, error) {
	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	net := os.Getenv("DBTEST_DOCKER_NETWORK")
	if net == "" {
		net = "dbtest-net"
	}
	return &dockerRunner{client: client, network: net, tel: tel}, nil
}

// Run creates and starts the container.
func (r *dockerRunner) Run(ctx context.Context, spec harness.Spec) (harness.Handle, error) {
	var netConfig *network.NetworkingConfig
	if r.network != "" {
		netConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{r.network: {}},
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
		// A retried activity finds its own container from the first attempt.
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

	// Starting an already-running container is a no-op.
	if err := r.client.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return harness.Handle{}, fmt.Errorf("container start: %w", err)
	}

	if r.tel != nil {
		r.tel.Logger.Info("started harness container",
			slog.String("container_id", id),
			slog.String("name", spec.Name),
			slog.String("image", spec.Image),
			slog.String("network", r.network),
		)
	}
	return harness.Handle{ID: id, SelfExits: spec.SelfExits}, nil
}

// Stop ends the container and returns what it printed. Every path collects and
// removes: elsewhere the output outlives the container in the platform's log
// service, and a runner that drops it on failure would hide the one thing worth
// reading.
func (r *dockerRunner) Stop(ctx context.Context, h harness.Handle) ([]byte, error) {
	err := r.terminate(ctx, h)
	var code int
	if err == nil {
		code, err = r.wait(ctx, h.ID)
	}

	// Docker keeps the output inside the container, so collect before removing.
	// Both run on a fresh context, since the failure being handled here is often
	// ctx expiring.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	out, collectErr := r.collect(cctx, h.ID)
	removeErr := r.remove(cctx, h.ID)

	switch {
	case err != nil:
		return out, err
	case code != 0:
		return out, fmt.Errorf("container exited %d: %s", code, tail(out))
	case collectErr != nil:
		return out, collectErr
	case removeErr != nil:
		return out, removeErr
	}
	return out, nil
}

// terminate asks a still-running container to exit. The grace period has to
// outlast one sample plus writing the result, or the container is killed
// mid-print and the output is lost.
func (r *dockerRunner) terminate(ctx context.Context, h harness.Handle) error {
	if h.SelfExits {
		return nil
	}
	timeout := 10
	if err := r.client.ContainerStop(ctx, h.ID, container.StopOptions{Timeout: &timeout}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("container stop: %w", err)
	}
	return nil
}

// wait blocks until the container is no longer running and reports its exit code.
func (r *dockerRunner) wait(ctx context.Context, id string) (int, error) {
	statusCh, errCh := r.client.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		// A container that is gone is not running, which is what this waits for.
		if err != nil && !errdefs.IsNotFound(err) {
			return 0, fmt.Errorf("container wait: %w", err)
		}
		return 0, nil
	case status := <-statusCh:
		return int(status.StatusCode), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// collect returns stdout and stderr interleaved in the order they were written.
func (r *dockerRunner) collect(ctx context.Context, id string) ([]byte, error) {
	rc, err := r.client.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		// A container that is gone printed nothing this call can still read.
		if errdefs.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("container logs: %w", err)
	}
	defer rc.Close()

	// Docker frames the two streams; StdCopy strips the framing. Writing both to
	// one buffer matches Fargate, where CloudWatch has already merged them.
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, rc); err != nil {
		return nil, fmt.Errorf("demultiplex logs: %w", err)
	}
	return out.Bytes(), nil
}

func (r *dockerRunner) remove(ctx context.Context, id string) error {
	if err := r.client.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("container remove: %w", err)
	}
	return nil
}

// tail returns the last few lines, for an error message.
func tail(out []byte) string {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return string(bytes.Join(lines, []byte("\n")))
}

func newRunner(tel *telemetry.Telemetry) (harness.Runner, error) {
	return New(tel)
}

func init() {
	harness.Register(harness.Docker, newRunner)
}

var _ harness.Runner = (*dockerRunner)(nil)
