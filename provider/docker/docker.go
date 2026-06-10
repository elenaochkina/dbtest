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

	info, err := p.client.ContainerInspect(ctx, resp.ID)
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("container inspect: %w", err)
	}

	bindings := info.NetworkSettings.Ports[nat.Port("5432/tcp")]
	if len(bindings) == 0 {
		return provider.ClusterInfo{}, fmt.Errorf("no host port assigned for 5432/tcp")
	}
	hostPort := bindings[0].HostPort

	dsn := fmt.Sprintf("postgres://postgres:test@localhost:%s/postgres", hostPort)

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
