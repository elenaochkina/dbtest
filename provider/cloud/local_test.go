package cloud

import (
	"context"
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/provider"
)

func TestLocalRegistersItself(t *testing.T) {
	if !slices.Contains(Names(), provider.Local) {
		t.Errorf("Names() = %v, want local", Names())
	}
}

func TestLocalCheckReportsDaemonDown(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	c, err := NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Status{State: provider.StateMisconfigured, Detail: "docker is not running"}
	if got := c.Check(context.Background()); got != want {
		t.Errorf("Check() = %+v, want %+v", got, want)
	}
}
