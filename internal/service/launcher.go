package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	dockerclient "github.com/docker/docker/client"
)

// State is a service's state.
type State string

const (
	StateStarting State = "starting"
	StateUp       State = "up"
	StateError    State = "error"
)

// Event reports that a service changed state. Detail may hold several
// lines, and a line may start with a label and a tab.
type Event struct {
	Name   string
	State  State
	Detail string
}

// service is something the launcher starts and checks
type service interface {
	start(ctx context.Context) (string, error)
	check(ctx context.Context) error
}

// watchedService is the launcher's record of one service.
type watchedService struct {
	name    string
	service service // starts and checks it
	retry   chan struct{}
	after   []chan struct{}
	up      chan struct{}
	markUp  func()
}

// Launcher starts the state db, the Temporal server, the telemetry
// service and, once those are up, the worker, then checks each one every 2 seconds.
type Launcher struct {
	docker    *dockerclient.Client
	telemetry *telemetryService
	worker    *workerService
	workerUp  chan struct{}
	services  []watchedService
	interval  time.Duration
	events    chan Event
	ctx       context.Context
	cancel    context.CancelFunc
	wait      sync.WaitGroup
}

func New(logs slog.Handler) (*Launcher, error) {
	docker, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	stateDB := newStateDB(docker, "dbtest-state")
	temporal := newTemporalServer(docker, "dbtest-temporal")
	telemetry := newTelemetryService(logs)
	worker := newWorkerService(stateDB, temporal, telemetry, logs)

	launcher := &Launcher{
		docker:    docker,
		telemetry: telemetry,
		worker:    worker,
		interval:  2 * time.Second,
		events:    make(chan Event, 16),
	}
	stateUp := launcher.add("state db", stateDB)
	temporalUp := launcher.add("temporal", temporal)
	telemetryUp := launcher.add("telemetry", telemetry)
	launcher.workerUp = launcher.add("worker", worker, stateUp, temporalUp, telemetryUp)
	return launcher, nil
}

// add adds a service that starts once every after signal is closed. It
// returns the service's up signal.
func (l *Launcher) add(name string, service service, after ...chan struct{}) chan struct{} {
	up := make(chan struct{})
	l.services = append(l.services, watchedService{
		name:    name,
		service: service,
		retry:   make(chan struct{}, 1),
		after:   after,
		up:      up,
		markUp:  sync.OnceFunc(func() { close(up) }),
	})
	return up
}

// Events returns the channel that receives every state change.
func (l *Launcher) Events() <-chan Event {
	return l.events
}

// Starts every service and its checks in the background.
func (l *Launcher) Start(ctx context.Context) {
	l.ctx, l.cancel = context.WithCancel(ctx)
	for _, watched := range l.services {
		l.wait.Go(func() { l.watch(l.ctx, watched) })
	}
}

// Starts again every service that is in the error state.
func (l *Launcher) Retry() {
	for _, watched := range l.services {
		select {
		case watched.retry <- struct{}{}:
		default:
		}
	}
}

// Close stops the checks, the worker and the telemetry service.
// Containers keep running.
func (l *Launcher) Close() {
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

// watch starts the service, then checks it on every tick and reports it
// whether down or back.
// retry starts it again if it is down.
func (l *Launcher) watch(ctx context.Context, watched watchedService) {
	for _, dependency := range watched.after {
		select {
		case <-dependency:
		case <-ctx.Done():
			return
		}
	}
	detail, up := l.startService(ctx, watched)
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-watched.retry:
			if !up {
				detail, up = l.startService(ctx, watched)
			}
		case <-ticker.C:
			if detail == "" {
				continue
			}
			err := watched.service.check(ctx)
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

// startService reports starting, starts the service, then reports up or
// error. It returns the service's address and whether it is up.
func (l *Launcher) startService(ctx context.Context, watched watchedService) (string, bool) {
	// tell a console that event has been started
	l.send(ctx, Event{Name: watched.name, State: StateStarting})
	detail, err := watched.service.start(ctx)
	if err != nil {
		l.send(ctx, Event{Name: watched.name, State: StateError, Detail: err.Error()})
		return "", false
	}

	watched.markUp()
	// service works, tells a console the address
	l.send(ctx, Event{Name: watched.name, State: StateUp, Detail: detail})
	return detail, true
}

// put an event into event channel
func (l *Launcher) send(ctx context.Context, event Event) {
	select {
	case l.events <- event:
	case <-ctx.Done():
	}
}
