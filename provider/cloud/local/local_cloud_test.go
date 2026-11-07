package local

import (
	"context"
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
)

func TestLocalRegistersItself(t *testing.T) {
	if !slices.Contains(cloud.Names(), provider.Local) {
		t.Errorf("cloud.Names() = %v, want local", cloud.Names())
	}
}

func TestLocalCheckReportsDaemonDown(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Status{State: provider.StateMisconfigured, Detail: "docker is not running"}
	if got := c.Check(context.Background()); got != want {
		t.Errorf("Check() = %+v, want %+v", got, want)
	}
}

func TestDockerImagesReadEnvironment(t *testing.T) {
	t.Setenv("DBTEST_PROBE_IMAGE", "")
	t.Setenv("DBTEST_BENCH_IMAGE", "dbtest/bench:test")

	want := harness.Images{Probe: "dbtest/probe:dev", Bench: "dbtest/bench:test"}
	if got := DockerImages(); got != want {
		t.Errorf("DockerImages() = %+v, want %+v", got, want)
	}
}

func TestNewRunnerRejectsOtherRunner(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.NewRunner(harness.Fargate, nil); err == nil {
		t.Fatal("NewRunner(fargate) returned no error on the local cloud")
	}
}
