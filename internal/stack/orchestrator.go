package stack

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	dockerclient "github.com/docker/docker/client"
)

// State is a component's state.
type State string

const (
	StateStarting State = "starting"
	StateUp       State = "up"
	StateError    State = "error"
)

// Event reports that a component changed state. Detail may hold several
// lines, and a line may start with a label and a tab.
type Event struct {
	Name   string
	State  State
	Detail string
}

// component is a part of the local stack the orchestrator starts and checks.
type component interface {
	start(ctx context.Context) (string, error)
	check(ctx context.Context) error
}

// watchedComponent is a component with its name, its retry signal, the up
// signals it waits for before its first start, and its own up signal, closed
// the first time it is up.
type watchedComponent struct {
	name      string
	component component
	retry     chan struct{}
	after     []chan struct{}
	up        chan struct{}
	markUp    func()
}

// Orchestrator starts the state db, the Temporal server, the telemetry
// service and, once those are up, the worker, then checks each one every 2 seconds.
type Orchestrator struct {
	docker     *dockerclient.Client
	telemetry  *telemetryService
	worker     *workerService
	components []watchedComponent
	interval   time.Duration
	events     chan Event
	cancel     context.CancelFunc
	wait       sync.WaitGroup
}

func New(logs slog.Handler) (*Orchestrator, error) {
	docker, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	orchestrator := &Orchestrator{
		docker:    docker,
		telemetry: newTelemetryService(logs),
		interval:  2 * time.Second,
		events:    make(chan Event, 16),
	}
	stateDB := newStateDB(docker, "dbtest-state")
	temporal := newTemporalServer(docker, "dbtest-temporal")
	orchestrator.worker = newWorkerService(stateDB, temporal, orchestrator.telemetry, logs)

	stateUp := orchestrator.add("state db", stateDB)
	temporalUp := orchestrator.add("temporal", temporal)
	telemetryUp := orchestrator.add("telemetry", orchestrator.telemetry)
	orchestrator.add("worker", orchestrator.worker, stateUp, temporalUp, telemetryUp)
	return orchestrator, nil
}

// add adds a component that starts once every after signal is closed. It
// returns the component's up signal.
func (o *Orchestrator) add(name string, component component, after ...chan struct{}) chan struct{} {
	up := make(chan struct{})
	o.components = append(o.components, watchedComponent{
		name:      name,
		component: component,
		retry:     make(chan struct{}, 1),
		after:     after,
		up:        up,
		markUp:    sync.OnceFunc(func() { close(up) }),
	})
	return up
}

// Events returns the channel that receives every state change.
func (o *Orchestrator) Events() <-chan Event {
	return o.events
}

// Starts every component and its checks in the background.
func (o *Orchestrator) Start(ctx context.Context) {
	ctx, o.cancel = context.WithCancel(ctx)
	for _, watched := range o.components {
		o.wait.Go(func() { o.watch(ctx, watched) })
	}
}

// Starts again every component that is in the error state.
func (o *Orchestrator) Retry() {
	for _, watched := range o.components {
		select {
		case watched.retry <- struct{}{}:
		default:
		}
	}
}

// Close stops the checks, the worker and the telemetry service. Containers
// keep running.
func (o *Orchestrator) Close() {
	if o.cancel != nil {
		o.cancel()
	}
	o.wait.Wait()
	if o.worker != nil {
		o.worker.stop()
	}
	if o.telemetry != nil {
		o.telemetry.stop()
	}
	if o.docker != nil {
		o.docker.Close()
	}
}

// watch starts the component, then checks it on every tick and reports it
// whether down or back.
// retry starts it again if it is down.
func (o *Orchestrator) watch(ctx context.Context, watched watchedComponent) {
	for _, dependency := range watched.after {
		select {
		case <-dependency:
		case <-ctx.Done():
			return
		}
	}
	detail, up := o.startComponent(ctx, watched)
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watched.retry:
			if !up {
				detail, up = o.startComponent(ctx, watched)
			}
		case <-ticker.C:
			if detail == "" {
				continue
			}
			err := watched.component.check(ctx)
			if up && err != nil {
				up = false
				o.send(ctx, Event{Name: watched.name, State: StateError, Detail: err.Error()})
			} else if !up && err == nil {
				up = true
				o.send(ctx, Event{Name: watched.name, State: StateUp, Detail: detail})
			}
		}
	}
}

// startComponent reports starting, starts the component, then reports up or
// error. It returns the component's address and whether it is up.
func (o *Orchestrator) startComponent(ctx context.Context, watched watchedComponent) (string, bool) {
	// tell a console that event has been started
	o.send(ctx, Event{Name: watched.name, State: StateStarting})
	detail, err := watched.component.start(ctx)
	if err != nil {
		o.send(ctx, Event{Name: watched.name, State: StateError, Detail: err.Error()})
		return "", false
	}
	// component works, tells a console the address
	watched.markUp()
	o.send(ctx, Event{Name: watched.name, State: StateUp, Detail: detail})
	return detail, true
}

// put an event into event channel
func (o *Orchestrator) send(ctx context.Context, event Event) {
	select {
	case o.events <- event:
	case <-ctx.Done():
	}
}
