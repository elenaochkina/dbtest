package service

import (
	"context"
	"embed"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

//go:embed prometheus
var prometheusFiles embed.FS

// prometheusService runs Prometheus in a Docker container on the shared network.
type prometheusService struct {
	docker  *dockerclient.Client
	name    string
	network string
	url     string
}

func newPrometheusService(docker *dockerclient.Client, name, network string) *prometheusService {
	return &prometheusService{docker: docker, name: name, network: network}
}

// start uses the container It returns the web URL.
func (s *prometheusService) start(ctx context.Context) (string, error) {
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
	archive, err := folderArchive(prometheusFiles, "prometheus")
	if err != nil {
		return "", err
	}
	if err := s.docker.CopyToContainer(ctx, s.name, "/etc/prometheus", archive, container.CopyToContainerOptions{}); err != nil {
		return "", fmt.Errorf("copy config to %s: %w", s.name, err)
	}
	if info.State.Running {
		if err := s.docker.ContainerKill(ctx, s.name, "SIGHUP"); err != nil {
			return "", fmt.Errorf("reload %s: %w", s.name, err)
		}
	} else {
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

	bindings := info.NetworkSettings.Ports[nat.Port("9090/tcp")]
	if len(bindings) == 0 {
		return "", fmt.Errorf("%s publishes no port for 9090", s.name)
	}
	s.url = "http://localhost:" + bindings[0].HostPort
	if err := s.waitReady(ctx); err != nil {
		return "", err
	}
	return s.url, nil
}

// create creates a new container from prom/prometheus on a free port,
// preferably 9090, keeping 90 days of data.
func (s *prometheusService) create(ctx context.Context) error {
	if _, err := s.docker.ImageInspect(ctx, "prom/prometheus:v3.15.0"); errdefs.IsNotFound(err) {
		reader, err := s.docker.ImagePull(ctx, "prom/prometheus:v3.15.0", image.PullOptions{})
		if err != nil {
			return fmt.Errorf("pull prom/prometheus:v3.15.0: %w", err)
		}
		io.Copy(io.Discard, reader)
		reader.Close()
	}
	port, err := freePort(9090)
	if err != nil {
		return err
	}
	if err := ensureNetwork(ctx, s.docker, s.network); err != nil {
		return err
	}
	_, err = s.docker.ContainerCreate(ctx,
		&container.Config{
			Image: "prom/prometheus:v3.15.0",
			Cmd: []string{"--config.file=/etc/prometheus/prometheus.yml",
				"--storage.tsdb.path=/prometheus", "--storage.tsdb.retention.time=90d"},
			ExposedPorts: nat.PortSet{"9090/tcp": {}},
			Labels:       map[string]string{"dbtest.managed": "true", "dbtest.role": "prometheus"},
		},
		&container.HostConfig{
			NetworkMode:  container.NetworkMode(s.network),
			PortBindings: nat.PortMap{"9090/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(port)}}},
			Mounts:       []mount.Mount{{Type: mount.TypeVolume, Source: s.name, Target: "/prometheus"}},
			ExtraHosts:   []string{"host.docker.internal:host-gateway"},
		},
		nil, nil, s.name)
	if err != nil {
		return fmt.Errorf("create %s: %w", s.name, err)
	}
	return nil
}

// waitReady checks every second, for up to 60 seconds, until Prometheus is ready.
func (s *prometheusService) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := s.check(ctx)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s not ready after 60s: %w", s.name, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// check asks Prometheus whether it is ready.I
func (s *prometheusService) check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+"/-/ready", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("prometheus returned %s", response.Status)
	}
	return nil
}
