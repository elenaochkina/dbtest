package cloud

import (
	"context"
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/provider"
)

func TestGCPRegistersItself(t *testing.T) {
	if !slices.Contains(Names(), provider.GCP) {
		t.Errorf("Names() = %v, want gcp", Names())
	}
}

func TestGCPCheckReportsMissingVariables(t *testing.T) {
	t.Setenv("GCP_PROJECT", "dbtest")
	t.Setenv("GCP_AUTHORIZED_NETWORKS", "")

	c, err := NewGCP()
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Status{State: provider.StateMisconfigured, Detail: "1 variable missing: run source .env (GCP_AUTHORIZED_NETWORKS)"}
	if got := c.Check(context.Background()); got != want {
		t.Errorf("Check() = %+v, want %+v", got, want)
	}
}

func TestGCPDefaultsRegion(t *testing.T) {
	t.Setenv("GCP_REGION", "")
	c, err := NewGCP()
	if err != nil {
		t.Fatal(err)
	}
	if region := c.(*GCP).Region(); region != "us-west1" {
		t.Errorf("Region() = %q, want us-west1", region)
	}
}
