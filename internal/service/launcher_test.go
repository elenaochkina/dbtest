package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeService struct {
	mutex      sync.Mutex
	startError error
	checkError error
}

func (f *fakeService) start(context.Context) (string, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.startError != nil {
		return "", f.startError
	}
	return "fake-address", nil
}

func (f *fakeService) check(context.Context) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.checkError
}

func (f *fakeService) set(startError, checkError error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.startError = startError
	f.checkError = checkError
}

func newTestLauncher(fake *fakeService) *Launcher {
	serviceLauncher := &Launcher{interval: 10 * time.Millisecond, events: make(chan Event, 16)}
	serviceLauncher.addService("fake", fake)
	return serviceLauncher
}

func expectEvent(t *testing.T, serviceLauncher *Launcher, want Event) {
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

func TestLauncherReportsHealthChanges(t *testing.T) {
	fake := &fakeService{}
	serviceLauncher := newTestLauncher(fake)
	serviceLauncher.Start(context.Background())
	defer serviceLauncher.Close()

	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateStarting})
	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateUp, Detail: "fake-address"})

	fake.set(nil, errors.New("connection refused"))
	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateError, Detail: "connection refused"})

	fake.set(nil, nil)
	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateUp, Detail: "fake-address"})
}

func TestLauncherStartsAfterDependencies(t *testing.T) {
	dependency := &fakeService{startError: errors.New("docker is not running")}
	dependent := &fakeService{}
	serviceLauncher := &Launcher{interval: 10 * time.Millisecond, events: make(chan Event, 16)}
	dependencyUp := serviceLauncher.addService("dependency", dependency)
	serviceLauncher.addService("dependent", dependent, dependencyUp)
	serviceLauncher.Start(context.Background())
	defer serviceLauncher.Close()

	expectEvent(t, serviceLauncher, Event{Name: "dependency", State: StateStarting})
	expectEvent(t, serviceLauncher, Event{Name: "dependency", State: StateError, Detail: "docker is not running"})

	dependency.set(nil, nil)
	serviceLauncher.RetryFailed()
	expectEvent(t, serviceLauncher, Event{Name: "dependency", State: StateStarting})
	expectEvent(t, serviceLauncher, Event{Name: "dependency", State: StateUp, Detail: "fake-address"})
	expectEvent(t, serviceLauncher, Event{Name: "dependent", State: StateStarting})
	expectEvent(t, serviceLauncher, Event{Name: "dependent", State: StateUp, Detail: "fake-address"})
}

func TestLauncherRetriesFailedStart(t *testing.T) {
	fake := &fakeService{startError: errors.New("docker is not running")}
	serviceLauncher := newTestLauncher(fake)
	serviceLauncher.Start(context.Background())
	defer serviceLauncher.Close()

	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateStarting})
	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateError, Detail: "docker is not running"})

	fake.set(nil, nil)
	serviceLauncher.RetryFailed()
	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateStarting})
	expectEvent(t, serviceLauncher, Event{Name: "fake", State: StateUp, Detail: "fake-address"})
}
