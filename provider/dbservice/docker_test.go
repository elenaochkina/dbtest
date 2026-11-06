package dbservice

import (
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/provider"
)

func TestDockerRegistersInLocal(t *testing.T) {
	descriptor, ok := Get(provider.Docker)
	if !ok || !slices.Equal(descriptor.Clouds, []provider.CloudName{provider.Local}) || descriptor.SupportsHighAvailability {
		t.Fatalf("Get(docker) = %+v, %v", descriptor, ok)
	}
}

func TestDockerDisruptions(t *testing.T) {
	got := dockerDisruptions(provider.ProvisionRequest{HighAvailability: true})
	if !slices.Equal(got, []provider.Disruption{provider.Restart, provider.Crash}) {
		t.Errorf("dockerDisruptions = %v", got)
	}
}

func TestNewDockerServiceRejectsOtherCloud(t *testing.T) {
	if _, err := NewDockerService(testCloud{name: provider.AWS}, nil); err == nil {
		t.Fatal("NewDockerService returned no error for a non-local cloud")
	}
}

func TestDockerResources(t *testing.T) {
	res := dockerResources(provider.ProvisionRequest{VCPU: 1.5, MemoryMiB: 512})
	if res.NanoCPUs != 1_500_000_000 || res.Memory != 512*1024*1024 {
		t.Errorf("dockerResources = %+v", res)
	}
}
