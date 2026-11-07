package dbservice

import (
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
)

func TestRDSRegistersInAWS(t *testing.T) {
	registration, ok := Get(provider.RDS)
	if !ok || !slices.Equal(registration.Clouds(), []provider.CloudName{provider.AWS}) || !registration.SupportsHighAvailability() {
		t.Fatalf("Get(rds) = %+v, %v", registration, ok)
	}
	if !cloud.Supports(provider.AWS, provider.RDS) {
		t.Error("aws does not support rds")
	}
}

func TestRDSDisruptions(t *testing.T) {
	if got := (rdsRegistration{}).Disruptions(provider.ProvisionRequest{}); !slices.Equal(got, []provider.Disruption{provider.Restart}) {
		t.Errorf("without a standby: %v", got)
	}
	got := (rdsRegistration{}).Disruptions(provider.ProvisionRequest{HighAvailability: true})
	if !slices.Equal(got, []provider.Disruption{provider.Restart, provider.Failover}) {
		t.Errorf("with a standby: %v", got)
	}
}

func TestRDSBuildRejectsOtherCloud(t *testing.T) {
	if _, err := (rdsRegistration{}).Build(testCloud{name: provider.GCP}, nil); err == nil {
		t.Fatal("Build returned no error for a non-AWS cloud")
	}
}

func TestResolveRDSClass(t *testing.T) {
	for _, tc := range []struct {
		req  provider.ProvisionRequest
		want string
	}{
		{provider.ProvisionRequest{VCPU: 2, MemoryMiB: 8192}, "db.m7i.large"},
		{provider.ProvisionRequest{VCPU: 4, MemoryMiB: 16384}, "db.m7i.xlarge"},
		{provider.ProvisionRequest{VCPU: 64, MemoryMiB: 1 << 20}, "db.m7i.2xlarge"},
	} {
		if got := resolveRDSClass(tc.req, ""); got != tc.want {
			t.Errorf("resolveRDSClass(%+v) = %s, want %s", tc.req, got, tc.want)
		}
	}
	if got := resolveRDSClass(provider.ProvisionRequest{}, "db.r6g.large"); got != "db.r6g.large" {
		t.Errorf("override ignored: %s", got)
	}
}
