package stack

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
)

// TestTemporalServerLifecycle creates, stops and restarts a throwaway Temporal
// container. It needs Docker and is skipped with -short.
func TestTemporalServerLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	ctx := context.Background()
	docker, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("dbtest-temporal-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		docker.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
		docker.VolumeRemove(ctx, name, true)
	})
	server := newTemporalServer(docker, name)

	if _, err := server.start(ctx); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := docker.ContainerStop(ctx, name, container.StopOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := server.check(ctx); err == nil {
		t.Fatal("check passed on a stopped container")
	}
	if _, err := server.start(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	detail, err := server.start(ctx)
	if err != nil {
		t.Fatalf("adopt running: %v", err)
	}
	t.Log(detail)
}
