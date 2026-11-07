package local

import (
	"context"
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
)

func TestLocalRegistersItself(t *testing.T) {
	if !slices.Contains(cloud.Names(), provider.Local) {
		t.Errorf("cloud.Names() = %v, want local", cloud.Names())
	}
}

func TestLocalCheckReportsDaemonDown(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Status{State: provider.StateMisconfigured, Detail: "docker is not running"}
	if got := c.Check(context.Background()); got != want {
		t.Errorf("Check() = %+v, want %+v", got, want)
	}
}
