package dbservice

import (
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
)

func TestAuroraRegistersInAWS(t *testing.T) {
	descriptor, ok := Get(provider.Aurora)
	if !ok || !slices.Equal(descriptor.Clouds, []provider.CloudName{provider.AWS}) || !descriptor.SupportsHighAvailability {
		t.Fatalf("Get(aurora) = %+v, %v", descriptor, ok)
	}
	if !cloud.Supports(provider.AWS, provider.Aurora) {
		t.Error("aws does not support aurora")
	}
}

func TestAuroraDisruptions(t *testing.T) {
	if got := auroraDisruptions(provider.ProvisionRequest{}); !slices.Equal(got, []provider.Disruption{provider.Restart, provider.Crash}) {
		t.Errorf("without a reader: %v", got)
	}
	got := auroraDisruptions(provider.ProvisionRequest{HighAvailability: true})
	if !slices.Equal(got, []provider.Disruption{provider.Restart, provider.Crash, provider.Failover}) {
		t.Errorf("with a reader: %v", got)
	}
}

func TestNewAuroraServiceRejectsOtherCloud(t *testing.T) {
	if _, err := NewAuroraService(testCloud{name: provider.GCP}, nil); err == nil {
		t.Fatal("NewAuroraService returned no error for a non-AWS cloud")
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
