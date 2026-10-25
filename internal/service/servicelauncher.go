package service

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

// component is a part of the local stack the service launcher starts and checks.
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

// ServiceLauncher starts the state db, the Temporal server, the telemetry
// service and, once those are up, the worker, then checks each one every 2 seconds.
type ServiceLauncher struct {
	docker     *dockerclient.Client
	telemetry  *telemetryService
	worker     *workerService
	workerUp   chan struct{}
	components []watchedComponent
	interval   time.Duration
	events     chan Event
	ctx        context.Context
	cancel     context.CancelFunc
	wait       sync.WaitGroup
}

func New(logs slog.Handler) (*ServiceLauncher, error) {
	docker, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	serviceLauncher := &ServiceLauncher{
		docker:    docker,
		telemetry: newTelemetryService(logs),
		interval:  2 * time.Second,
		events:    make(chan Event, 16),
	}
	stateDB := newStateDB(docker, "dbtest-state")
	temporal := newTemporalServer(docker, "dbtest-temporal")
	serviceLauncher.worker = newWorkerService(stateDB, temporal, serviceLauncher.telemetry, logs)

	stateUp := serviceLauncher.add("state db", stateDB)
	temporalUp := serviceLauncher.add("temporal", temporal)
	telemetryUp := serviceLauncher.add("telemetry", serviceLauncher.telemetry)
	serviceLauncher.workerUp = serviceLauncher.add("worker", serviceLauncher.worker, stateUp, temporalUp, telemetryUp)
	return serviceLauncher, nil
}

// add adds a component that starts once every after signal is closed. It
// returns the component's up signal.
func (l *ServiceLauncher) add(name string, component component, after ...chan struct{}) chan struct{} {
	up := make(chan struct{})
	l.components = append(l.components, watchedComponent{
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
func (l *ServiceLauncher) Events() <-chan Event {
	return l.events
}

// Starts every component and its checks in the background.
func (l *ServiceLauncher) Start(ctx context.Context) {
	l.ctx, l.cancel = context.WithCancel(ctx)
	for _, watched := range l.components {
		l.wait.Go(func() { l.watch(l.ctx, watched) })
	}
}

// Starts again every component that is in the error state.
func (l *ServiceLauncher) Retry() {
	for _, watched := range l.components {
		select {
		case watched.retry <- struct{}{}:
		default:
		}
	}
}

// Close stops the checks, the worker and the telemetry service. Containers
// keep running.
func (l *ServiceLauncher) Close() {
	if l.cancel != nil {
		l.cancel()
	}
	l.wait.Wait()
	if l.worker != nil {
		l.worker.stop()
	}
	if l.telemetry != nil {
		l.telemetry.stop()
	}
	if l.docker != nil {
		l.docker.Close()
	}
}

// watch starts the component, then checks it on every tick and reports it
// whether down or back.
// retry starts it again if it is down.
func (l *ServiceLauncher) watch(ctx context.Context, watched watchedComponent) {
	for _, dependency := range watched.after {
		select {
		case <-dependency:
		case <-ctx.Done():
			return
		}
	}
	detail, up := l.startComponent(ctx, watched)
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watched.retry:
			if !up {
				detail, up = l.startComponent(ctx, watched)
			}
		case <-ticker.C:
			if detail == "" {
				continue
			}
			err := watched.component.check(ctx)
			if up && err != nil {
				up = false
				l.send(ctx, Event{Name: watched.name, State: StateError, Detail: err.Error()})
			} else if !up && err == nil {
				up = true
				l.send(ctx, Event{Name: watched.name, State: StateUp, Detail: detail})
			}
		}
	}
}

// startComponent reports starting, starts the component, then reports up or
// error. It returns the component's address and whether it is up.
func (l *ServiceLauncher) startComponent(ctx context.Context, watched watchedComponent) (string, bool) {
	// tell a console that event has been started
	l.send(ctx, Event{Name: watched.name, State: StateStarting})
	detail, err := watched.component.start(ctx)
	if err != nil {
		l.send(ctx, Event{Name: watched.name, State: StateError, Detail: err.Error()})
		return "", false
	}
	// component works, tells a console the address
	watched.markUp()
	l.send(ctx, Event{Name: watched.name, State: StateUp, Detail: detail})
	return detail, true
}

// put an event into event channel
func (l *ServiceLauncher) send(ctx context.Context, event Event) {
	select {
	case l.events <- event:
	case <-ctx.Done():
	}
}
