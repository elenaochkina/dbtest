package stack

import (
	"context"
	"testing"
)

func TestTelemetryServiceLifecycle(t *testing.T) {
	ctx := context.Background()
	service := newTelemetryService()

	url, err := service.start(ctx)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Logf("metrics at %s", url)

	if again, err := service.start(ctx); err != nil || again != url {
		t.Fatalf("second start = %q, %v; want %q, nil", again, err, url)
	}

	service.stop()
	if err := service.check(ctx); err == nil {
		t.Fatal("check passed after stop")
	}
}
