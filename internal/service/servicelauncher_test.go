package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeComponent struct {
	mutex      sync.Mutex
	startError error
	checkError error
}

func (f *fakeComponent) start(context.Context) (string, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.startError != nil {
		return "", f.startError
	}
	return "fake-address", nil
}

func (f *fakeComponent) check(context.Context) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.checkError
}

func (f *fakeComponent) set(startError, checkError error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.startError = startError
	f.checkError = checkError
}

func newTestServiceLauncher(fake *fakeComponent) *ServiceLauncher {
	serviceLauncher := &ServiceLauncher{interval: 10 * time.Millisecond, events: make(chan Event, 16)}
	serviceLauncher.add("fake", fake)
	return serviceLauncher
}

func expectEvent(t *testing.T, serviceLauncher *ServiceLauncher, want Event) {
	t.Helper()
	select {
	case got := <-serviceLauncher.Events():
		if got != want {
			t.Fatalf("event = %+v, want %+v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no event, want %+v", want)
	}
}

func TestServiceLauncherReportsHealthChanges(t *testing.T) {
	fake := &fakeComponent{}
	serviceLauncher := newTestServiceLauncher(fake)
	serviceLauncher.Start(context.Background())
	defer serviceLauncher.Close()

	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateStarting})
	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateUp, Detail: "fake-address"})

	fake.set(nil, errors.New("connection refused"))
	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateError, Detail: "connection refused"})

	fake.set(nil, nil)
	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateUp, Detail: "fake-address"})
}

func TestServiceLauncherStartsAfterDependencies(t *testing.T) {
	dependency := &fakeComponent{startError: errors.New("docker is not running")}
	dependent := &fakeComponent{}
	serviceLauncher := &ServiceLauncher{interval: 10 * time.Millisecond, events: make(chan Event, 16)}
	dependencyUp := serviceLauncher.add("dependency", dependency)
	serviceLauncher.add("dependent", dependent, dependencyUp)
	serviceLauncher.Start(context.Background())
	defer serviceLauncher.Close()

	expectEvent(t, serviceLauncher, Event{Name: "dependency", State: StateStarting})
	expectEvent(t, serviceLauncher, Event{Name: "dependency", State: StateError, Detail: "docker is not running"})

	dependency.set(nil, nil)
	serviceLauncher.Retry()
	expectEvent(t, serviceLauncher, Event{Name: "dependency", State: StateStarting})
	expectEvent(t, serviceLauncher, Event{Name: "dependency", State: StateUp, Detail: "fake-address"})
	expectEvent(t, serviceLauncher, Event{Name: "dependent", State: StateStarting})
	expectEvent(t, serviceLauncher, Event{Name: "dependent", State: StateUp, Detail: "fake-address"})
}

func TestServiceLauncherRetriesFailedStart(t *testing.T) {
	fake := &fakeComponent{startError: errors.New("docker is not running")}
	serviceLauncher := newTestServiceLauncher(fake)
	serviceLauncher.Start(context.Background())
	defer serviceLauncher.Close()

	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateStarting})
	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateError, Detail: "docker is not running"})

	fake.set(nil, nil)
	serviceLauncher.Retry()
	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateStarting})
	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateUp, Detail: "fake-address"})
}
