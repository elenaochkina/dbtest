package dbservice

import (
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
)

func TestRDSRegistersInAWS(t *testing.T) {
	descriptor, ok := Get(provider.RDS)
	if !ok || !slices.Equal(descriptor.Clouds, []provider.CloudName{provider.AWS}) || !descriptor.SupportsHighAvailability {
		t.Fatalf("Get(rds) = %+v, %v", descriptor, ok)
	}
	if !cloud.Supports(provider.AWS, provider.RDS) {
		t.Error("aws does not support rds")
	}
}

func TestRDSDisruptions(t *testing.T) {
	if got := rdsDisruptions(provider.ProvisionRequest{}); !slices.Equal(got, []provider.Disruption{provider.Restart}) {
		t.Errorf("without a standby: %v", got)
	}
	got := rdsDisruptions(provider.ProvisionRequest{HighAvailability: true})
	if !slices.Equal(got, []provider.Disruption{provider.Restart, provider.Failover}) {
		t.Errorf("with a standby: %v", got)
	}
}

func TestNewRDSServiceRejectsOtherCloud(t *testing.T) {
	if _, err := NewRDSService(testCloud{name: provider.GCP}, nil); err == nil {
		t.Fatal("NewRDSService returned no error for a non-AWS cloud")
	}
}

func TestResolveRDSClass(t *testing.T) {
	for _, tc := range []struct {
		req  provider.ProvisionRequest
		want string
	}{
		{provider.ProvisionRequest{VCPU: 2, MemoryMiB: 2048}, "db.t3.small"},
		{provider.ProvisionRequest{VCPU: 4, MemoryMiB: 8192}, "db.t3.xlarge"},
		{provider.ProvisionRequest{VCPU: 64, MemoryMiB: 1 << 20}, "db.m6g.2xlarge"},
	} {
		if got := resolveRDSClass(tc.req, ""); got != tc.want {
			t.Errorf("resolveRDSClass(%+v) = %s, want %s", tc.req, got, tc.want)
		}
	}
	if got := resolveRDSClass(provider.ProvisionRequest{}, "db.r6g.large"); got != "db.r6g.large" {
		t.Errorf("override ignored: %s", got)
	}
}
