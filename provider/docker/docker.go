package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/jackc/pgx/v5"
)

type dockerProvider struct {
	client *dockerclient.Client
	image  string
	tel    *telemetry.Telemetry
}

// New creates a Docker provider.
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

// The password parameter is ignored by Docker — it is fixed at "test". The
// token is used as the container name for idempotency
func (p *dockerProvider) Provision(ctx context.Context, req provider.ProvisionRequest, token, _ string) (provider.ClusterInfo, error) {
	start := time.Now()

	if req.VCPU < 0 || req.MemoryMiB < 0 {
		return provider.ClusterInfo{}, fmt.Errorf("invalid provision request: negative resource (vcpu=%v memory_mib=%d)", req.VCPU, req.MemoryMiB)
	}

	// Pull the image so ContainerCreate never fails on a cold machine.
	reader, err := p.client.ImagePull(ctx, p.image, image.PullOptions{})
	if err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("image pull: %w", err)
	}
	io.Copy(io.Discard, reader)
	reader.Close()

	// A named container on a shared network is what lets bench and probe address
	// the database as <name>:5432 — an address that survives the restart
	// the published host port, which PublishAllPorts reassigns on every start.
	name := containerName(token)
	if err := p.ensureNetwork(ctx); err != nil {
		return provider.ClusterInfo{}, err
	}

	resp, err := p.client.ContainerCreate(ctx,
		&container.Config{
			Image: p.image,
			Env:   []string{"POSTGRES_PASSWORD=test", "POSTGRES_DB=postgres"},
		},
		&container.HostConfig{
			PublishAllPorts: true,
			Resources:       dockerResources(req),
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{networkName: {}},
		},
		nil, name)

	id := resp.ID
	if err != nil {
		//Docker adoption: try to find a same name container
		if name == "" || !errdefs.IsConflict(err) {
			return provider.ClusterInfo{}, fmt.Errorf("container create %q: %w", name, err)
		}
		existing, ierr := p.client.ContainerInspect(ctx, name)
		if ierr != nil {
			return provider.ClusterInfo{}, fmt.Errorf("adopt existing container %q: %w", name, ierr)
		}
		id = existing.ID
		if p.tel != nil {
			p.tel.Logger.Info("adopted container from a previous attempt",
				slog.String("container_id", id),
				slog.String("name", name),
			)
		}
	}

	// Starting an already-running container is a no-op
	if err := p.client.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("container start: %w", err)
	}

	hostPort, err := p.hostPort(ctx, id)
	if err != nil {
		return provider.ClusterInfo{}, err
	}

	if p.tel != nil {
		p.tel.Metrics.ProviderProvisionDuration.WithLabelValues("docker").Observe(time.Since(start).Seconds())
		p.tel.Logger.Info("provisioned cluster",
			slog.String("container_id", id),
			slog.String("host_port", hostPort),
			slog.Float64("vcpu", req.VCPU),
			slog.Int("memory_mib", req.MemoryMiB),
		)
	}

	return provider.ClusterInfo{
		ID:       id,
		Target:   targetForPort(hostPort),
		Internal: targetForContainer(name),
		Password: "test",
	}, nil
}

// networkName is a single long-lived network shared by every run.
const networkName = "dbtest-net"

// ensureNetwork creates the shared network if it does not already exist.
func (p *dockerProvider) ensureNetwork(ctx context.Context) error {
	_, err := p.client.NetworkInspect(ctx, networkName, network.InspectOptions{})
	if err == nil {
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect network %q: %w", networkName, err)
	}
	if _, err := p.client.NetworkCreate(ctx, networkName, network.CreateOptions{}); err != nil {
		// A concurrent run may have won the race; that is success, not failure.
		if !errdefs.IsConflict(err) {
			return fmt.Errorf("create network %q: %w", networkName, err)
		}
	}
	return nil
}

// containerName derives a stable name from the provisioning token
func containerName(token string) string {
	if token == "" {
		return "" // no stable identity supplied — let Docker pick
	}
	return "dbtest-" + token
}

// dockerResources maps the cross-provider ProvisionRequest onto Docker's cgroup
// controls.
func dockerResources(req provider.ProvisionRequest) container.Resources {
	var res container.Resources
	if req.VCPU > 0 {
		res.NanoCPUs = int64(req.VCPU * 1e9)
	}
	if req.MemoryMiB > 0 {
		res.Memory = int64(req.MemoryMiB) * 1024 * 1024
	}
	return res
}

func (p *dockerProvider) WaitForReady(ctx context.Context, cluster provider.ClusterInfo) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		connCtx, cancel := context.WithTimeout(ctx, time.Second)
		conn, err := pgx.Connect(connCtx, cluster.Target.URL(cluster.Password))
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

// uses for init() as a parameter
func newProvider(tel *telemetry.Telemetry) (provider.Provider, error) {
	return New(tel)
}

func init() {
	provider.Register(provider.Docker, newProvider)
}

// KillProcess injects a forced, ungraceful failure: it SIGKILLs the container's
// main process (postgres) to simulate a crash, waits for it to exit, then starts
// it again — so the database comes back through WAL crash recovery rather than a
// clean shutdown.
func (p *dockerProvider) KillProcess(ctx context.Context, cluster provider.ClusterInfo) (provider.ClusterInfo, error) {
	start := time.Now()

	if err := p.client.ContainerKill(ctx, cluster.ID, "SIGKILL"); err != nil {
		return provider.ClusterInfo{}, fmt.Errorf("container kill: %w", err)
	}

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

	return provider.ClusterInfo{
		ID:       cluster.ID,
		Target:   targetForPort(hostPort),
		Internal: cluster.Internal,
		Password: cluster.Password,
	}, nil
}

// Compile-time assertion that dockerProvider satisfies the core Provider contract.
var _ provider.Provider = (*dockerProvider)(nil)

// Compile-time assertion that the docker provider supports failure injection.
var _ provider.FailureInjector = (*dockerProvider)(nil)

// hostPort inspects the container and returns the host port mapped to Postgres
// 5432/tcp.
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

// targetForPort builds the provider-agnostic PGTarget for a container whose
// Postgres is published on hostPort. This is the worker's route, from outside
// Docker.
func targetForPort(hostPort string) provider.PGTarget {
	port, _ := strconv.Atoi(hostPort)
	return provider.PGTarget{
		Host:     "localhost",
		Port:     port,
		Database: "postgres",
		User:     "postgres",
	}
}

// targetForContainer builds the route a sibling container usesa
func targetForContainer(name string) provider.PGTarget {
	return provider.PGTarget{
		Host:     name,
		Port:     5432,
		Database: "postgres",
		User:     "postgres",
	}
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
