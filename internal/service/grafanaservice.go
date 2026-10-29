package service

import (
	"archive/tar"
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

//go:embed grafana
var grafanaFiles embed.FS

// grafanaService runs Grafana in a Docker container on the shared network.
type grafanaService struct {
	docker  *dockerclient.Client
	name    string
	network string
	url     string
}

func newGrafanaService(docker *dockerclient.Client, name, network string) *grafanaService {
	return &grafanaService{docker: docker, name: name, network: network}
}

// start uses the container if it is running, starts it if it is stopped, or
// creates it if it is missing, then waits until Grafana answers health
// checks. It returns the web URL.
func (s *grafanaService) start(ctx context.Context) (string, error) {
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
	if err := s.copyProvisioning(ctx); err != nil {
		return "", err
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

	bindings := info.NetworkSettings.Ports[nat.Port("3000/tcp")]
	if len(bindings) == 0 {
		return "", fmt.Errorf("%s publishes no port for 3000", s.name)
	}
	s.url = "http://localhost:" + bindings[0].HostPort
	if err := s.waitReady(ctx); err != nil {
		return "", err
	}
	return s.url, nil
}

// create creates a new container from grafana/grafana-oss on a free port,
// preferably 3000, with anonymous admin access.
func (s *grafanaService) create(ctx context.Context) error {
	if _, err := s.docker.ImageInspect(ctx, "grafana/grafana-oss:13.0.2"); errdefs.IsNotFound(err) {
		reader, err := s.docker.ImagePull(ctx, "grafana/grafana-oss:13.0.2", image.PullOptions{})
		if err != nil {
			return fmt.Errorf("pull grafana/grafana-oss:13.0.2: %w", err)
		}
		io.Copy(io.Discard, reader)
		reader.Close()
	}
	port, err := freePort(3000)
	if err != nil {
		return err
	}
	if err := ensureNetwork(ctx, s.docker, s.network); err != nil {
		return err
	}
	_, err = s.docker.ContainerCreate(ctx,
		&container.Config{
			Image: "grafana/grafana-oss:13.0.2",
			Env: []string{
				"GF_AUTH_ANONYMOUS_ENABLED=true",
				"GF_AUTH_ANONYMOUS_ORG_ROLE=Admin",
				"GF_AUTH_DISABLE_LOGIN_FORM=true",
			},
			ExposedPorts: nat.PortSet{"3000/tcp": {}},
			Labels:       map[string]string{"dbtest.managed": "true", "dbtest.role": "grafana"},
		},
		&container.HostConfig{
			NetworkMode:  container.NetworkMode(s.network),
			PortBindings: nat.PortMap{"3000/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(port)}}},
			Mounts:       []mount.Mount{{Type: mount.TypeVolume, Source: s.name, Target: "/var/lib/grafana"}},
		},
		nil, nil, s.name)
	if err != nil {
		return fmt.Errorf("create %s: %w", s.name, err)
	}
	return nil
}

// copyProvisioning copies the embedded provisioning files into the container,
// replacing the ones there.
func (s *grafanaService) copyProvisioning(ctx context.Context) error {
	archive, err := provisioningArchive()
	if err != nil {
		return err
	}
	if err := s.docker.CopyToContainer(ctx, s.name, "/etc/grafana/provisioning", archive, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copy provisioning to %s: %w", s.name, err)
	}
	return nil
}

// provisioningArchive packs the embedded grafana folder into a tar archive,
// with paths relative to that folder.
func provisioningArchive() (io.Reader, error) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	err := fs.WalkDir(grafanaFiles, "grafana", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := grafanaFiles.ReadFile(path)
		if err != nil {
			return err
		}
		header := &tar.Header{Name: strings.TrimPrefix(path, "grafana/"), Mode: 0o644, Size: int64(len(content))}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		_, err = writer.Write(content)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("pack grafana provisioning: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return &buffer, nil
}

// waitReady checks every second, for up to 60 seconds, until Grafana answers
// health checks.
func (s *grafanaService) waitReady(ctx context.Context) error {
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

// check asks Grafana's health endpoint.
func (s *grafanaService) check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+"/api/health", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("grafana returned %s", response.Status)
	}
	return nil
}
