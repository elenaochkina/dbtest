# How the console starts a workflow

How `serviceLauncher`, `workflows.go`, `recovery.go` and `model.go` connect, from program
start to a running recovery workflow.

## Diagram

```
cmd/dbtest/main.go
│  1. serviceLauncher := service.New(...)         ── creates
│  2. serviceLauncher.Start(...)                  ── starts services
│  3. console.New(serviceLauncher, logs)          ── hands it to the console
▼
┌──────────────────────── internal/console ─────────────────────────┐
│                                                                    │
│  model.go                                                          │
│    New() copies three functions out of serviceLauncher:            │
│      events        = serviceLauncher.Events()                      │
│      retry         = serviceLauncher.RetryFailed                   │
│      startRecovery = serviceLauncher.StartRecovery  ◄──────┐       │
│    model.form  (recoveryForm)                              │       │
│        │                                                   │       │
│        ▼                                                   │       │
│  keys.go                                                   │       │
│    enter on "start recovery" → m.form = openRecoveryForm() │       │
│    keys while form open      → handleFormKey               │       │
│    enter on "confirm"        → startRecovery(m.startRecovery, form.cfg)
│        │                                                   │       │
│        ▼                                                   │       │
│  recovery_form.go                                          │       │
│    form.cfg starts as defaultRecovery() ──────────┐        │       │
│    each step changes part of form.cfg             │        │       │
│                                                   ▼        │       │
│  recovery.go                                               │       │
│    defaultRecovery()  → the starting config                │       │
│    startRecovery(start, cfg) → a tea.Cmd that calls start(cfg)     │
└──────────────────────────────┼─────────────────────────────────────┘
                               │ start(cfg) = serviceLauncher.StartRecovery(cfg)
                               ▼
┌──────────────────────── internal/service ─────────────────────────┐
│  workflows.go                                                      │
│    StartRecovery(cfg):                                             │
│      a. worker up?  (waits on workerUp signal, else error)         │
│      b. temporalClient.ExecuteWorkflow(RecoveryWorkflow, cfg)      │
│      c. slog.Info("workflow started")                              │
│      d. background: run.Get → "workflow completed" / "failed"      │
│                                                                    │
│  launcher.go                                                       │
│    owns: workerUp signal, worker (with temporalClient), ctx, wait  │
└──────────────────────────────┼─────────────────────────────────────┘
                               ▼
                 Temporal server (container) → worker in this process
                               ▼
              temporal/workflows/recovery_workflow.go  runs with cfg
```

## Each file's job

| File | Package | Job |
|---|---|---|
| `main.go` | main | Creates and starts the service launcher, then gives it to the console. |
| `launcher.go` | service | Runs the services. It holds the `worker` (and its Temporal client) and the `workerUp` signal. |
| `workflows.go` | service | `StartRecovery`: uses the worker's Temporal client to start a workflow and logs the result. |
| `model.go` | console | The screen's state. It stores `StartRecovery` as a plain function in `m.startRecovery`. |
| `keys.go` | console | Decides what each key does: open the form, move between steps, start on confirm. |
| `recovery_form.go` | console | The steps. It builds `form.cfg` from your choices. |
| `recovery.go` | console | Supplies the default config and wraps `start(cfg)` as a Bubble Tea command. |

## Three things to notice

1. **The console never imports Temporal.** It only holds `m.startRecovery`, a function. The
   console doesn't know what happens inside it, which is why tests can swap in a fake that just
   records the config.
2. **`cfg` travels unchanged.** It starts in `defaultRecovery()`, the form edits it, and
   `startRecovery` → `StartRecovery` → `ExecuteWorkflow` deliver it to `RecoveryWorkflow`.
3. **`workflows.go` needs the service launcher's internals.** That's why `StartRecovery` lives
   in `service`, not in the console. It needs the worker's `temporalClient` and the `workerUp`
   signal, and both are private to `service`.
