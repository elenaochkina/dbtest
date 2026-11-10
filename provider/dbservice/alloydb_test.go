package dbservice

import (
	"errors"
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	gcpcloud "github.com/elenaochkina/dbtest/provider/cloud/gcp"
	"google.golang.org/api/googleapi"
)

func TestAlloyDBRegistersInGCP(t *testing.T) {
	registration, ok := Get(provider.AlloyDB)
	if !ok || !slices.Equal(registration.Clouds(), []provider.CloudName{provider.GCP}) || !registration.SupportsHighAvailability() {
		t.Fatalf("Get(alloydb) = %+v, %v", registration, ok)
	}
	if !cloud.Supports(provider.GCP, provider.AlloyDB) {
		t.Error("gcp does not support alloydb")
	}
}

func TestAlloyDBDisruptions(t *testing.T) {
	if got := (alloyDBRegistration{}).Disruptions(provider.ProvisionRequest{}); !slices.Equal(got, []provider.Disruption{provider.Restart, provider.Crash}) {
		t.Errorf("without a standby: %v", got)
	}
	got := (alloyDBRegistration{}).Disruptions(provider.ProvisionRequest{HighAvailability: true})
	if !slices.Equal(got, []provider.Disruption{provider.Restart, provider.Crash, provider.Failover}) {
		t.Errorf("with a standby: %v", got)
	}
}

func TestAlloyDBBuildRejectsOtherCloud(t *testing.T) {
	if _, err := (alloyDBRegistration{}).Build(testCloud{name: provider.AWS}, nil); err == nil {
		t.Fatal("Build returned no error for a non-GCP cloud")
	}
}

func TestAlloyDBBuildNeedsProject(t *testing.T) {
	t.Setenv("GCP_PROJECT", "")
	t.Setenv("GCP_AUTHORIZED_NETWORKS", "203.0.113.7/32")
	c, err := gcpcloud.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (alloyDBRegistration{}).Build(c, nil); err == nil {
		t.Fatal("Build returned no error without GCP_PROJECT")
	}
}

func TestAlloyDBCPUCount(t *testing.T) {
	for _, tc := range []struct {
		req  provider.ProvisionRequest
		want int64
	}{
		{provider.ProvisionRequest{VCPU: 2, MemoryMiB: 8192}, 2},
		{provider.ProvisionRequest{VCPU: 1, MemoryMiB: 1024}, 2},
		{provider.ProvisionRequest{VCPU: 3, MemoryMiB: 8192}, 4},
		{provider.ProvisionRequest{VCPU: 2, MemoryMiB: 65536}, 8},
		{provider.ProvisionRequest{VCPU: 200, MemoryMiB: 8192}, 128},
	} {
		if got := alloyDBCPUCount(tc.req); got != tc.want {
			t.Errorf("alloyDBCPUCount(%+v) = %d, want %d", tc.req, got, tc.want)
		}
	}
}

func TestIsFailoverLease(t *testing.T) {
	lease := &googleapi.Error{Code: 400, Message: "validateFailoverRequest failed: failover lease validation failed: generic::failed_precondition: another failover operation was recently executed on this instance. Please try again after: Mon Nov  9 23:09:45 UTC 2026"}
	if !isFailoverLease(lease) {
		t.Error("lease rejection not recognized")
	}
	if isFailoverLease(&googleapi.Error{Code: 400, Message: "invalid instance"}) || isFailoverLease(errors.New("timeout")) {
		t.Error("other errors recognized as a lease")
	}
}
