package cloud

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
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
func (c testCloud) Supports(s provider.DBServiceName) bool { return Supports(c.name, s) }

func TestNewCloudProviderBuildsRegisteredCloud(t *testing.T) {
	Register("test-new", func() (Cloud, error) { return testCloud{name: "test-new"}, nil })

	built, err := NewCloudProvider("test-new")
	if err != nil {
		t.Fatal(err)
	}
	if built.Name() != "test-new" {
		t.Errorf("Name() = %q, want test-new", built.Name())
	}
}

func TestNewCloudProviderRejectsUnknownCloud(t *testing.T) {
	if _, err := NewCloudProvider("test-missing"); err == nil {
		t.Fatal("NewCloudProvider returned no error for an unknown cloud")
	}
}

func TestNamesAreSorted(t *testing.T) {
	Register("test-b", func() (Cloud, error) { return testCloud{name: "test-b"}, nil })
	Register("test-a", func() (Cloud, error) { return testCloud{name: "test-a"}, nil })

	names := Names()
	if !slices.IsSorted(names) || !slices.Contains(names, "test-a") || !slices.Contains(names, "test-b") {
		t.Errorf("Names() = %v", names)
	}
}

func TestSupportsFollowsHost(t *testing.T) {
	Host("test-host", "test-service")

	if !Supports("test-host", "test-service") {
		t.Error("Supports(test-host, test-service) = false, want true")
	}
	if Supports("test-host", "test-other") {
		t.Error("Supports(test-host, test-other) = true, want false")
	}
	if Supports("test-elsewhere", "test-service") {
		t.Error("Supports(test-elsewhere, test-service) = true, want false")
	}
}
