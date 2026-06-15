package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/jackc/pgx/v5"
)

type dockerProvider struct {
	client   *dockerclient.Client
	image string
	tel   *telemetry.Telemetry
}

// New creates a Docker provider. It reads DOCKER_PG_IMAGE for the Postgres image
func New(tel *telemetry.Telemetry) (*dockerProvider, error) {
	img := os.Getenv("DOCKER_PG_IMAGE")
	if img == "" {
		img = "postgres:16"
	}

	client, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	return &dockerProvider{client: client, image: img, tel: tel}, nil
}

func (p *dockerProvider) Provision(ctx context.Context) (provider.ClusterInfo, error) {
	start := time.Now()

	// Pull the image so ContainerCreate never fails on a cold machine.
	reader, err := p.client.ImagePull(ctx, p.image, image.PullOptions{})
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("image pull: %w", err)
	}
	io.Copy(io.Discard, reader)
	reader.Close()

	resp, err := p.client.ContainerCreate(ctx,
		&container.Config{
			Image: p.image,
			Env:   []string{"POSTGRES_PASSWORD=test", "POSTGRES_DB=postgres"},
		},
		&container.HostConfig{
			PublishAllPorts: true,
		},
		nil, nil, "")
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("container create: %w", err)
	}

	if err := p.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("container start: %w", err)
	}

	hostPort, err := p.hostPort(ctx, resp.ID)
	if err != nil {
		return provider.ClusterInfo{}, err
	}
	dsn := dsnForPort(hostPort)

	if p.tel != nil {
		p.tel.Metrics.ProviderProvisionDuration.WithLabelValues("docker").Observe(time.Since(start).Seconds())
		p.tel.Logger.Info("provisioned cluster",
			slog.String("container_id", resp.ID),
			slog.String("host_port", hostPort),
		)
	}

	return provider.ClusterInfo{ID: resp.ID, DSN: dsn}, nil
}

func (p *dockerProvider) WaitForReady(ctx context.Context, cluster provider.ClusterInfo) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		connCtx, cancel := context.WithTimeout(ctx, time.Second)
		conn, err := pgx.Connect(connCtx, cluster.DSN)
		cancel()
		if err == nil {
			conn.Close(context.Background())
			if p.tel != nil {
				p.tel.Logger.Info("cluster is ready",
					slog.String("container_id", cluster.ID),
				)
			}
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("cluster %s did not become ready within 30s", cluster.ID)
}

func (p *dockerProvider) Deprovision(ctx context.Context, clusterID string) error {
	var lastErr error
	for attempt := range 3 {
		lastErr = p.deprovision(ctx, clusterID)
		if lastErr == nil {
			break
		}
		if errdefs.IsNotFound(lastErr) {
			lastErr = nil // container already gone — treat as success
			break
		}
		if p.tel != nil {
			p.tel.Logger.Warn("deprovision attempt failed",
				slog.Int("attempt", attempt+1),
				slog.String("container_id", clusterID),
				slog.Any("error", lastErr),
			)
		}
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		return lastErr
	}
	if p.tel != nil {
		p.tel.Metrics.ProviderDeprovisionTotal.WithLabelValues("docker").Inc()
		p.tel.Logger.Info("deprovisioned cluster",
			slog.String("container_id", clusterID),
		)
	}
	return nil
}

func init() {
	provider.Register(provider.Docker, func(tel *telemetry.Telemetry) (provider.Provider, error) {
		return New(tel)
	})
}

// Restart performs a forced, ungraceful restart: it SIGKILLs the container's
// main process (postgres) to simulate a crash, waits for it to exit, then starts
// it again — so the database comes back through WAL crash recovery rather than a
// clean shutdown. It returns the refreshed ClusterInfo, since PublishAllPorts can
// re-publish the host port across a restart. Waiting until the database accepts
// connections again is the caller's job (WaitForReady).
func (p *dockerProvider) Restart(ctx context.Context, cluster provider.ClusterInfo) (provider.ClusterInfo, error) {
	start := time.Now()

	if err := p.client.ContainerKill(ctx, cluster.ID, "SIGKILL"); err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("container kill: %w", err)
	}

	// Wait for the container to actually exit before starting it, so the start
	// does not race the kill.
	statusCh, errCh := p.client.ContainerWait(ctx, cluster.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return provider.ClusterInfo{}, fmt.Errorf("wait for kill: %w", err)
		}
	case <-statusCh:
	case <-ctx.Done():
		return provider.ClusterInfo{}, ctx.Err()
	}

	if err := p.client.ContainerStart(ctx, cluster.ID, container.StartOptions{}); err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("container start: %w", err)
	}

	hostPort, err := p.hostPort(ctx, cluster.ID)
	if err != nil {
		return provider.ClusterInfo{}, err
	}

	if p.tel != nil {
		p.tel.Logger.Info("force-restarted cluster (SIGKILL)",
			slog.String("container_id", cluster.ID),
			slog.String("host_port", hostPort),
			slog.Duration("took", time.Since(start)),
		)
	}
	return provider.ClusterInfo{ID: cluster.ID, DSN: dsnForPort(hostPort)}, nil
}

// Compile-time assertion that the docker provider supports restart.
var _ provider.Restarter = (*dockerProvider)(nil)

// hostPort inspects the container and returns the host port mapped to Postgres
// 5432/tcp. With PublishAllPorts this is assigned at start and can change across
// a restart, so callers re-read it rather than caching.
func (p *dockerProvider) hostPort(ctx context.Context, containerID string) (string, error) {
	info, err := p.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("container inspect: %w", err)
	}
	bindings := info.NetworkSettings.Ports[nat.Port("5432/tcp")]
	if len(bindings) == 0 {
		return "", fmt.Errorf("no host port assigned for 5432/tcp")
	}
	return bindings[0].HostPort, nil
}

func dsnForPort(hostPort string) string {
	return fmt.Sprintf("postgres://postgres:test@localhost:%s/postgres", hostPort)
}

// deprovision performs a single stop+remove attempt.
func (p *dockerProvider) deprovision(ctx context.Context, clusterID string) error {
	timeout := 5
	if err := p.client.ContainerStop(ctx, clusterID, container.StopOptions{Timeout: &timeout}); err != nil {
		if !errdefs.IsNotFound(err) {
			return fmt.Errorf("container stop: %w", err)
		}
	}
	if err := p.client.ContainerRemove(ctx, clusterID, container.RemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	}); err != nil {
		return fmt.Errorf("container remove: %w", err)
	}
	return nil
}
