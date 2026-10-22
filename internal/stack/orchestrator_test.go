package stack

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

func newTestOrchestrator(fake *fakeComponent) *Orchestrator {
	orchestrator := &Orchestrator{interval: 10 * time.Millisecond, events: make(chan Event, 16)}
	orchestrator.add("fake", fake)
	return orchestrator
}

func expectEvent(t *testing.T, orchestrator *Orchestrator, want Event) {
	t.Helper()
	select {
	case got := <-orchestrator.Events():
		if got != want {
			t.Fatalf("event = %+v, want %+v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no event, want %+v", want)
	}
}

func TestOrchestratorReportsHealthChanges(t *testing.T) {
	fake := &fakeComponent{}
	orchestrator := newTestOrchestrator(fake)
	orchestrator.Start(context.Background())
	defer orchestrator.Close()

	expectEvent(t, orchestrator, Event{Name: "fake", State: StateStarting})
	expectEvent(t, orchestrator, Event{Name: "fake", State: StateUp, Detail: "fake-address"})

	fake.set(nil, errors.New("connection refused"))
	expectEvent(t, orchestrator, Event{Name: "fake", State: StateError, Detail: "connection refused"})

	fake.set(nil, nil)
	expectEvent(t, orchestrator, Event{Name: "fake", State: StateUp, Detail: "fake-address"})
}

func TestOrchestratorStartsAfterDependencies(t *testing.T) {
	dependency := &fakeComponent{startError: errors.New("docker is not running")}
	dependent := &fakeComponent{}
	orchestrator := &Orchestrator{interval: 10 * time.Millisecond, events: make(chan Event, 16)}
	dependencyUp := orchestrator.add("dependency", dependency)
	orchestrator.add("dependent", dependent, dependencyUp)
	orchestrator.Start(context.Background())
	defer orchestrator.Close()

	expectEvent(t, orchestrator, Event{Name: "dependency", State: StateStarting})
	expectEvent(t, orchestrator, Event{Name: "dependency", State: StateError, Detail: "docker is not running"})

	dependency.set(nil, nil)
	orchestrator.Retry()
	expectEvent(t, orchestrator, Event{Name: "dependency", State: StateStarting})
	expectEvent(t, orchestrator, Event{Name: "dependency", State: StateUp, Detail: "fake-address"})
	expectEvent(t, orchestrator, Event{Name: "dependent", State: StateStarting})
	expectEvent(t, orchestrator, Event{Name: "dependent", State: StateUp, Detail: "fake-address"})
}

func TestOrchestratorRetriesFailedStart(t *testing.T) {
	fake := &fakeComponent{startError: errors.New("docker is not running")}
	orchestrator := newTestOrchestrator(fake)
	orchestrator.Start(context.Background())
	defer orchestrator.Close()

	expectEvent(t, orchestrator, Event{Name: "fake", State: StateStarting})
	expectEvent(t, orchestrator, Event{Name: "fake", State: StateError, Detail: "docker is not running"})

	fake.set(nil, nil)
	orchestrator.Retry()
	expectEvent(t, orchestrator, Event{Name: "fake", State: StateStarting})
	expectEvent(t, orchestrator, Event{Name: "fake", State: StateUp, Detail: "fake-address"})
}
