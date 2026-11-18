package dbservice

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
	"github.com/elenaochkina/dbtest/provider/cloud"
	localcloud "github.com/elenaochkina/dbtest/provider/cloud/local"
	"github.com/elenaochkina/dbtest/telemetry"
)

// dockerservice runs Postgres in a local Docker container.
type dockerservice struct {
	client *dockerclient.Client
	image  string
	cloud  cloud.Cloud
	tel    *telemetry.Telemetry
}

// dockerRegistration registers Docker Postgres; Build returns the service that provisions it.
type dockerRegistration struct{}

func (dockerRegistration) Name() provider.DBServiceName { return provider.Docker }

func (dockerRegistration) Clouds() []provider.CloudName { return []provider.CloudName{provider.Local} }

func (dockerRegistration) SupportsHighAvailability() bool { return false }

// Build builds Postgres in Docker on the local cloud.
func (dockerRegistration) Build(c cloud.Cloud, tel *telemetry.Telemetry) (DBService, error) {
	local, ok := c.(*localcloud.LocalCloud)
	if !ok {
		return nil, fmt.Errorf("docker needs the local cloud, got %s", c.Name())
	}
	img := os.Getenv("DOCKER_PG_IMAGE")
	if img == "" {
		img = "postgres:18"
	}
	return &dockerservice{client: local.Client(), image: img, cloud: c, tel: tel}, nil
}

// The password parameter is ignored by Docker — it is fixed at "test". The
// token is used as the container name for idempotency
func (p *dockerservice) Provision(ctx context.Context, req provider.ProvisionRequest, token, _ string) (provider.ClusterInfo, error) {
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
	name := token // empty lets Docker pick
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
			EndpointsConfig: map[string]*network.EndpointSettings{dockerNetwork: {}},
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
		ID:              id,
		PublicConnInfo:  publicConnInfo(hostPort),
		PrivateConnInfo: privateConnInfo(name),
		Password:        "test",
	}, nil
}

// dockerNetwork is a single long-lived network shared by every run.
const dockerNetwork = "dbtest-net"

// ensureNetwork creates the shared network if it does not already exist.
func (p *dockerservice) ensureNetwork(ctx context.Context) error {
	_, err := p.client.NetworkInspect(ctx, dockerNetwork, network.InspectOptions{})
	if err == nil {
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect network %q: %w", dockerNetwork, err)
	}
	if _, err := p.client.NetworkCreate(ctx, dockerNetwork, network.CreateOptions{}); err != nil {
		// A concurrent run may have won the race; that is success, not failure.
		if !errdefs.IsConflict(err) {
			return fmt.Errorf("create network %q: %w", dockerNetwork, err)
		}
	}
	return nil
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

// WaitForReady waits until the container answers the probe over the Docker network.
func (p *dockerservice) WaitForReady(ctx context.Context, cluster provider.ClusterInfo) error {
	return waitForConnection(ctx, p.cloud, p.tel, cluster)
}

func (p *dockerservice) Deprovision(ctx context.Context, clusterID string) error {
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

// Disruptions lists what a container can be put through.
func (dockerRegistration) Disruptions(provider.ProvisionRequest) []provider.Disruption {
	return []provider.Disruption{provider.Restart, provider.Crash}
}

// SwitchoverReadiness reports true: a container accepts a disruption once it is running.
func (p *dockerservice) SwitchoverReadiness(context.Context, provider.ClusterInfo) (bool, error) {
	return true, nil
}

// Disrupt stops the container and starts it again, gracefully for Restart and
// with a SIGKILL for Crash, so that Crash comes back through WAL recovery rather
// than a clean shutdown.
func (p *dockerservice) Disrupt(ctx context.Context, cluster provider.ClusterInfo, disruption provider.Disruption) (provider.ClusterInfo, error) {
	start := time.Now()

	switch disruption {
	case provider.Restart:
		timeout := 30
		if err := p.client.ContainerRestart(ctx, cluster.ID, container.StopOptions{Timeout: &timeout}); err != nil {
			return provider.ClusterInfo{}, fmt.Errorf("container restart: %w", err)
		}
	case provider.Crash:
		if err := p.client.ContainerKill(ctx, cluster.ID, "SIGKILL"); err != nil {
			return provider.ClusterInfo{}, fmt.Errorf("container kill: %w", err)
		}
		if err := p.waitStopped(ctx, cluster.ID); err != nil {
			return provider.ClusterInfo{}, err
		}
		if err := p.client.ContainerStart(ctx, cluster.ID, container.StartOptions{}); err != nil {
			return provider.ClusterInfo{}, fmt.Errorf("container start: %w", err)
		}
	default:
		return provider.ClusterInfo{}, fmt.Errorf("docker cannot %s a container", disruption)
	}

	// PublishAllPorts reassigns the host port on every start, so the caller's
	// copy of the connection info is stale.
	hostPort, err := p.hostPort(ctx, cluster.ID)
	if err != nil {
		return provider.ClusterInfo{}, err
	}

	if p.tel != nil {
		p.tel.Logger.Info("disrupted cluster",
			slog.String("disruption", string(disruption)),
			slog.String("container_id", cluster.ID),
			slog.String("host_port", hostPort),
			slog.Duration("took", time.Since(start)),
		)
	}

	return provider.ClusterInfo{
		ID:              cluster.ID,
		PublicConnInfo:  publicConnInfo(hostPort),
		PrivateConnInfo: cluster.PrivateConnInfo,
		Password:        cluster.Password,
	}, nil
}

func (p *dockerservice) waitStopped(ctx context.Context, containerID string) error {
	statusCh, errCh := p.client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("wait for stop: %w", err)
		}
		return nil
	case <-statusCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func init() {
	Register(dockerRegistration{})
}

var _ DBService = (*dockerservice)(nil)

// hostPort inspects the container and returns the host port mapped to Postgres
// 5432/tcp.
func (p *dockerservice) hostPort(ctx context.Context, containerID string) (string, error) {
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

// publicConnInfo builds the connection info for a container whose
// Postgres is published on hostPort. This is the worker's route, from outside
// Docker.
func publicConnInfo(hostPort string) provider.PGConnInfo {
	port, _ := strconv.Atoi(hostPort)
	return provider.PGConnInfo{
		Host:     "localhost",
		Port:     port,
		Database: "postgres",
		User:     "postgres",
	}
}

// privateConnInfo builds the connection info a sibling container uses
func privateConnInfo(name string) provider.PGConnInfo {
	return provider.PGConnInfo{
		Host:     name,
		Port:     5432,
		Database: "postgres",
		User:     "postgres",
	}
}

// deprovision performs a single stop+remove attempt.
func (p *dockerservice) deprovision(ctx context.Context, clusterID string) error {
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
