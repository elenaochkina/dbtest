package service

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/jackc/pgx/v5"
)

// stateDB runs the state Postgres in a Docker container.
type stateDB struct {
	docker *dockerclient.Client
	name   string
	dsn    string
}

func newStateDB(docker *dockerclient.Client, name string) *stateDB {
	return &stateDB{docker: docker, name: name}
}

// start uses the container if it is running, starts it if it is stopped, or
// creates it if it is missing, then waits until Postgres accepts connections.
// It returns the DSN.
func (s *stateDB) start(ctx context.Context) (string, error) {
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

	bindings := info.NetworkSettings.Ports[nat.Port("5432/tcp")]
	if len(bindings) == 0 {
		return "", fmt.Errorf("%s publishes no port for 5432", s.name)
	}
	s.dsn = fmt.Sprintf("postgres://postgres:test@localhost:%s/postgres", bindings[0].HostPort)

	if err := s.waitReady(ctx); err != nil {
		return "", err
	}
	return s.dsn, nil
}

// create creates a new container from postgres:18 on a free port, preferably 5433.
func (s *stateDB) create(ctx context.Context) error {
	if _, err := s.docker.ImageInspect(ctx, "postgres:18"); errdefs.IsNotFound(err) {
		reader, err := s.docker.ImagePull(ctx, "postgres:18", image.PullOptions{})
		if err != nil {
			return fmt.Errorf("pull postgres:18: %w", err)
		}
		io.Copy(io.Discard, reader)
		reader.Close()
	}

	port, err := freePort(5433)
	if err != nil {
		return err
	}
	_, err = s.docker.ContainerCreate(ctx,
		&container.Config{
			Image:        "postgres:18",
			Env:          []string{"POSTGRES_PASSWORD=test"},
			ExposedPorts: nat.PortSet{"5432/tcp": {}},
			Labels:       map[string]string{"dbtest.managed": "true", "dbtest.role": "state"},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{"5432/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(port)}}},
			Mounts:       []mount.Mount{{Type: mount.TypeVolume, Source: s.name, Target: "/var/lib/postgresql"}},
		},
		nil, nil, s.name)
	if err != nil {
		return fmt.Errorf("create %s: %w", s.name, err)
	}
	return nil
}

// waitReady checks every half second, for up to 30 seconds, until Postgres
// accepts connections.
func (s *stateDB) waitReady(ctx context.Context) error {
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

// check connects to Postgres and pings it.
func (s *stateDB) check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		return err
	}
	defer connection.Close(ctx)
	return connection.Ping(ctx)
}
