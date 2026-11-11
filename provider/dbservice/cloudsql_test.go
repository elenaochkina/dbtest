package dbservice

import (
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	gcpcloud "github.com/elenaochkina/dbtest/provider/cloud/gcp"
)

func TestCloudSQLRegistersInGCP(t *testing.T) {
	registration, ok := Get(provider.CloudSQL)
	if !ok || !slices.Equal(registration.Clouds(), []provider.CloudName{provider.GCP}) || !registration.SupportsHighAvailability() {
		t.Fatalf("Get(cloudsql) = %+v, %v", registration, ok)
	}
	if !cloud.Supports(provider.GCP, provider.CloudSQL) {
		t.Error("gcp does not support cloudsql")
	}
}

func TestCloudSQLDisruptions(t *testing.T) {
	if got := (cloudSQLRegistration{}).Disruptions(provider.ProvisionRequest{}); !slices.Equal(got, []provider.Disruption{provider.Restart}) {
		t.Errorf("without a standby: %v", got)
	}
	got := (cloudSQLRegistration{}).Disruptions(provider.ProvisionRequest{HighAvailability: true})
	if !slices.Equal(got, []provider.Disruption{provider.Restart, provider.Failover}) {
		t.Errorf("with a standby: %v", got)
	}
}

func TestCloudSQLBuildRejectsOtherCloud(t *testing.T) {
	if _, err := (cloudSQLRegistration{}).Build(testCloud{name: provider.AWS}, nil); err == nil {
		t.Fatal("Build returned no error for a non-GCP cloud")
	}
}

func TestCloudSQLBuildNeedsProject(t *testing.T) {
	t.Setenv("GCP_PROJECT", "")
	t.Setenv("GCP_AUTHORIZED_NETWORKS", "203.0.113.7/32")
	c, err := gcpcloud.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (cloudSQLRegistration{}).Build(c, nil); err == nil {
		t.Fatal("Build returned no error without GCP_PROJECT")
	}
}

func TestResolveCloudSQLTier(t *testing.T) {
	for _, tc := range []struct {
		req  provider.ProvisionRequest
		want string
	}{
		{provider.ProvisionRequest{VCPU: 2, MemoryMiB: 8192}, "db-custom-2-8192"},
		{provider.ProvisionRequest{VCPU: 1, MemoryMiB: 2048}, "db-custom-1-3840"},
		{provider.ProvisionRequest{VCPU: 3, MemoryMiB: 4096}, "db-custom-4-4096"},
		{provider.ProvisionRequest{VCPU: 1, MemoryMiB: 16384}, "db-custom-4-16384"},
		{provider.ProvisionRequest{VCPU: 2, MemoryMiB: 5000}, "db-custom-2-5120"},
	} {
		if got := resolveCloudSQLTier(tc.req, ""); got != tc.want {
			t.Errorf("resolveCloudSQLTier(%+v) = %s, want %s", tc.req, got, tc.want)
		}
	}
	if got := resolveCloudSQLTier(provider.ProvisionRequest{}, "db-perf-optimized-N-2"); got != "db-perf-optimized-N-2" {
		t.Errorf("override ignored: %s", got)
	}
}

func TestGCPPostgresVersion(t *testing.T) {
	for version, want := range map[string]string{"18": "POSTGRES_18", "16.4": "POSTGRES_16"} {
		if got, err := gcpPostgresVersion(version); err != nil || got != want {
			t.Errorf("gcpPostgresVersion(%q) = %q, %v, want %q", version, got, err, want)
		}
	}
	if _, err := gcpPostgresVersion(""); err == nil {
		t.Error("gcpPostgresVersion(\"\") returned no error")
	}
}
