package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
)

// TestPrometheusServiceLifecycle creates, reloads, stops and restarts a
// throwaway Prometheus container. It needs Docker and is skipped with -short.
func TestPrometheusServiceLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	ctx := context.Background()
	docker, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("dbtest-prometheus-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		docker.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
		docker.VolumeRemove(ctx, name, true)
	})
	server := newPrometheusService(docker, name, loadConfig().Network)

	if _, err := server.start(ctx); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := server.start(ctx); err != nil {
		t.Fatalf("reload running: %v", err)
	}
	if err := docker.ContainerStop(ctx, name, container.StopOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := server.check(ctx); err == nil {
		t.Fatal("check passed on a stopped container")
	}
	url, err := server.start(ctx)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Log("url:", url)
}
