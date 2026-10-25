package service

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
)

// TestWorkerServiceLifecycle starts a worker against throwaway state db and
// Temporal containers. It needs Docker and is skipped with -short.
func TestWorkerServiceLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	ctx := context.Background()
	docker, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	stateName := fmt.Sprintf("dbtest-state-test-%d", suffix)
	temporalName := fmt.Sprintf("dbtest-temporal-test-%d", suffix)
	t.Cleanup(func() {
		for _, name := range []string{stateName, temporalName} {
			docker.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
			docker.VolumeRemove(ctx, name, true)
		}
	})

	stateDB := newStateDB(docker, stateName)
	temporal := newTemporalServer(docker, temporalName)
	telemetry := newTelemetryService(slog.DiscardHandler)
	for _, dependency := range []component{stateDB, temporal, telemetry} {
		if _, err := dependency.start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	defer telemetry.stop()

	worker := newWorkerService(stateDB, temporal, telemetry, slog.DiscardHandler)
	detail, err := worker.start(ctx)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Log(detail)
	if again, err := worker.start(ctx); err != nil || again != detail {
		t.Fatalf("second start = %q, %v; want %q, nil", again, err, detail)
	}

	if err := docker.ContainerStop(ctx, stateName, container.StopOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := worker.check(ctx); err == nil {
		t.Fatal("check passed with the state db stopped")
	}
	worker.stop()
}
