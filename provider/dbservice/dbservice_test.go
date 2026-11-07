package dbservice

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/telemetry"
)

type testCloud struct{ name provider.CloudName }

func (c testCloud) Name() provider.CloudName { return c.name }
func (c testCloud) Check(context.Context) provider.Status {
	return provider.Status{State: provider.StateReady}
}
func (c testCloud) Runners() []harness.RunnerName { return []harness.RunnerName{harness.Docker} }
func (c testCloud) NewRunner(harness.RunnerName, *telemetry.Telemetry) (harness.Runner, error) {
	return nil, errors.New("test cloud runs no containers")
}
func (c testCloud) Images(harness.RunnerName) harness.Images { return harness.Images{} }
func (c testCloud) Supports(s provider.DBServiceName) bool   { return cloud.Supports(c.name, s) }

type testService struct{}

func (testService) Provision(context.Context, provider.ProvisionRequest, string, string) (provider.ClusterInfo, error) {
	return provider.ClusterInfo{}, nil
}
func (testService) WaitForReady(context.Context, provider.ClusterInfo) error { return nil }
func (testService) Deprovision(context.Context, string) error                { return nil }
func (testService) Disrupt(_ context.Context, cluster provider.ClusterInfo, _ provider.Disruption) (provider.ClusterInfo, error) {
	return cluster, nil
}

// testRegistration is a service that runs in the given clouds and fails over
// only with a standby.
type testRegistration struct {
	name   provider.DBServiceName
	clouds []provider.CloudName
}

func (r testRegistration) Name() provider.DBServiceName { return r.name }
func (r testRegistration) Clouds() []provider.CloudName { return r.clouds }
func (testRegistration) SupportsHighAvailability() bool { return true }
func (testRegistration) Disruptions(req provider.ProvisionRequest) []provider.Disruption {
	if req.HighAvailability {
		return []provider.Disruption{provider.Restart, provider.Failover}
	}
	return []provider.Disruption{provider.Restart}
}
func (testRegistration) Build(cloud.Cloud, *telemetry.Telemetry) (DBService, error) {
	return testService{}, nil
}

// register adds a testRegistration.
func register(name provider.DBServiceName, clouds ...provider.CloudName) {
	Register(testRegistration{name: name, clouds: clouds})
}

func TestRegisterHostsServiceInItsClouds(t *testing.T) {
	register("test-hosted", "test-cloud-a", "test-cloud-b")

	for _, name := range []provider.CloudName{"test-cloud-a", "test-cloud-b"} {
		if !cloud.Supports(name, "test-hosted") {
			t.Errorf("cloud.Supports(%s, test-hosted) = false, want true", name)
		}
	}
	if cloud.Supports("test-cloud-c", "test-hosted") {
		t.Error("cloud.Supports(test-cloud-c, test-hosted) = true, want false")
	}
}

func TestNewDBServiceBuildsServiceInItsCloud(t *testing.T) {
	register("test-new", "test-cloud-new")

	service, err := NewDBService("test-new", testCloud{name: "test-cloud-new"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.(testService); !ok {
		t.Errorf("NewDBService returned %T, want testService", service)
	}
}

func TestNewDBServiceRejectsServiceInOtherCloud(t *testing.T) {
	register("test-placed", "test-cloud-home")

	if _, err := NewDBService("test-placed", testCloud{name: "test-cloud-away"}, nil); err == nil {
		t.Fatal("NewDBService returned no error for a cloud the service does not run in")
	}
}

func TestNewDBServiceRejectsUnknownService(t *testing.T) {
	if _, err := NewDBService("test-missing", testCloud{name: "test-cloud-any"}, nil); err == nil {
		t.Fatal("NewDBService returned no error for an unknown service")
	}
}

func TestDisruptionsFollowRequest(t *testing.T) {
	register("test-disrupted", "test-cloud-disrupted")

	if got := Disruptions("test-disrupted", provider.ProvisionRequest{}); !slices.Equal(got, []provider.Disruption{provider.Restart}) {
		t.Errorf("without a standby: %v, want [restart]", got)
	}
	got := Disruptions("test-disrupted", provider.ProvisionRequest{HighAvailability: true})
	if !slices.Equal(got, []provider.Disruption{provider.Restart, provider.Failover}) {
		t.Errorf("with a standby: %v, want [restart failover]", got)
	}
	if got := Disruptions("test-missing", provider.ProvisionRequest{}); got != nil {
		t.Errorf("unknown service: %v, want none", got)
	}
}

func TestGetAndNames(t *testing.T) {
	register("test-b", "test-cloud-names")
	register("test-a", "test-cloud-names")

	registration, ok := Get("test-a")
	if !ok || !registration.SupportsHighAvailability() {
		t.Errorf("Get(test-a) = %+v, %v", registration, ok)
	}
	if _, ok := Get("test-missing"); ok {
		t.Error("Get(test-missing) found a service")
	}
	names := Names()
	if !slices.IsSorted(names) || !slices.Contains(names, "test-a") || !slices.Contains(names, "test-b") {
		t.Errorf("Names() = %v", names)
	}
}
