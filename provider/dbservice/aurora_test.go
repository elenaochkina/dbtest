package dbservice

import (
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
)

func TestAuroraRegistersInAWS(t *testing.T) {
	registration, ok := Get(provider.Aurora)
	if !ok || !slices.Equal(registration.Clouds(), []provider.CloudName{provider.AWS}) || !registration.SupportsHighAvailability() {
		t.Fatalf("Get(aurora) = %+v, %v", registration, ok)
	}
	if !cloud.Supports(provider.AWS, provider.Aurora) {
		t.Error("aws does not support aurora")
	}
}

func TestAuroraDisruptions(t *testing.T) {
	if got := (auroraRegistration{}).Disruptions(provider.ProvisionRequest{}); !slices.Equal(got, []provider.Disruption{provider.Restart, provider.Crash}) {
		t.Errorf("without a reader: %v", got)
	}
	got := (auroraRegistration{}).Disruptions(provider.ProvisionRequest{HighAvailability: true})
	if !slices.Equal(got, []provider.Disruption{provider.Restart, provider.Crash, provider.Failover}) {
		t.Errorf("with a reader: %v", got)
	}
}

func TestAuroraBuildRejectsOtherCloud(t *testing.T) {
	if _, err := (auroraRegistration{}).Build(testCloud{name: provider.GCP}, nil); err == nil {
		t.Fatal("Build returned no error for a non-AWS cloud")
	}
}

func TestResolveAuroraClass(t *testing.T) {
	if got := resolveAuroraClass(provider.ProvisionRequest{VCPU: 2, MemoryMiB: 4096}, ""); got != "db.r6g.large" {
		t.Errorf("smallest: %s", got)
	}
	if got := resolveAuroraClass(provider.ProvisionRequest{VCPU: 64}, ""); got != "db.r6g.4xlarge" {
		t.Errorf("largest: %s", got)
	}
	if got := resolveAuroraClass(provider.ProvisionRequest{}, "db.r7g.large"); got != "db.r7g.large" {
		t.Errorf("override ignored: %s", got)
	}
}
