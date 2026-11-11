package dbservice

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	gcpcloud "github.com/elenaochkina/dbtest/provider/cloud/gcp"
	"google.golang.org/api/googleapi"
	sqladmin "google.golang.org/api/sqladmin/v1"
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

func TestCloudSQLAddresses(t *testing.T) {
	inst := &sqladmin.DatabaseInstance{IpAddresses: []*sqladmin.IpMapping{
		{Type: "PRIMARY", IpAddress: "34.82.1.2"},
		{Type: "OUTGOING", IpAddress: "34.82.9.9"},
		{Type: "PRIVATE", IpAddress: "10.0.80.5"},
	}}
	if publicIP, privateIP := cloudSQLAddresses(inst); publicIP != "34.82.1.2" || privateIP != "10.0.80.5" {
		t.Errorf("cloudSQLAddresses() = %q, %q; want 34.82.1.2, 10.0.80.5", publicIP, privateIP)
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

func TestStandbyRecovered(t *testing.T) {
	now := time.Date(2026, 11, 10, 4, 30, 0, 0, time.UTC)
	done := func(kind string, ago time.Duration) *sqladmin.Operation {
		return &sqladmin.Operation{OperationType: kind, Status: "DONE", EndTime: now.Add(-ago).Format(time.RFC3339)}
	}
	for _, tc := range []struct {
		name string
		ops  []*sqladmin.Operation
		want bool
	}{
		{"no operations", nil, true},
		{"running operation", []*sqladmin.Operation{{OperationType: "BACKUP_VOLUME", Status: "RUNNING"}}, false},
		{"failover 2m ago", []*sqladmin.Operation{done("FAILOVER", 2*time.Minute)}, false},
		{"failover 4m ago", []*sqladmin.Operation{done("FAILOVER", 4*time.Minute), done("CREATE", 10*time.Minute)}, true},
		{"backup 1m ago", []*sqladmin.Operation{done("BACKUP_VOLUME", time.Minute)}, true},
	} {
		if got := standbyRecovered(tc.ops, now); got != tc.want {
			t.Errorf("%s: standbyRecovered() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsInvalidState(t *testing.T) {
	invalid := &googleapi.Error{Code: 409, Message: "The instance or operation is not in an appropriate state to handle the request.", Errors: []googleapi.ErrorItem{{Reason: "invalidState"}}}
	if !isInvalidState(invalid) {
		t.Error("409 invalidState not recognized")
	}
	for _, err := range []error{
		&googleapi.Error{Code: 409, Errors: []googleapi.ErrorItem{{Reason: "operationInProgress"}}},
		&googleapi.Error{Code: 400, Errors: []googleapi.ErrorItem{{Reason: "invalidState"}}},
		errors.New("timeout"),
		nil,
	} {
		if isInvalidState(err) {
			t.Errorf("isInvalidState(%v) = true", err)
		}
	}
}
