package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
)

func TestGCPRegistersItself(t *testing.T) {
	if !slices.Contains(cloud.Names(), provider.GCP) {
		t.Errorf("cloud.Names() = %v, want gcp", cloud.Names())
	}
}

func TestGCPCheckReportsMissingVariables(t *testing.T) {
	t.Setenv("GCP_PROJECT", "dbtest")
	t.Setenv("GCP_AUTHORIZED_NETWORKS", "")

	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Status{State: provider.StateMisconfigured, Detail: "1 variable missing: run source .env (GCP_AUTHORIZED_NETWORKS)"}
	if got := c.Check(context.Background()); got != want {
		t.Errorf("Check() = %+v, want %+v", got, want)
	}
}

func TestGCPRunsOnCloudRunOnly(t *testing.T) {
	t.Setenv("GCP_CLOUDRUN_PROBE_JOB", "")
	t.Setenv("GCP_CLOUDRUN_BENCH_JOB", "")
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Runners(); !slices.Equal(got, []harness.RunnerName{harness.CloudRun}) {
		t.Errorf("Runners() = %v, want [cloudrun]", got)
	}
	if _, err := c.NewRunner(harness.Docker, nil); err == nil {
		t.Error("NewRunner(docker) returned no error")
	}
	want := harness.Images{Probe: harness.ProbeName, Bench: harness.BenchName}
	if got := c.Images(harness.CloudRun); got != want {
		t.Errorf("Images() = %+v, want %+v", got, want)
	}
}

func TestGCPDefaultsRegion(t *testing.T) {
	t.Setenv("GCP_REGION", "")
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if region := c.(*GCPCloud).Region(); region != "us-west1" {
		t.Errorf("Region() = %q, want us-west1", region)
	}
}
