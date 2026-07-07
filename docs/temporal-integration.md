# Temporal Integration — Design

How `dbtest` adopts [Temporal](https://temporal.io) durable workflows to run
scenarios that survive process crashes, and — critically — **never leak a paying
cloud cluster**.

This is a design doc, not a tutorial. It assumes the reader knows Go and the
`dbtest` scenario/provider layout but not Temporal, so it opens with terminology.

---

## Why Temporal

`scenario.Run` (`scenario/scenario_engine.go`) is an in-process step sequencer.
It builds a teardown stack (`onCleanup`) and drains it in reverse on exit
(`scenario_engine.go:127`) — but the stack lives in RAM. Its own comment says it:

> "does not survive a process crash." — `scenario_engine.go:105`

Our providers provision **real, billable resources** — the AWS RDS provider calls
`CreateDBInstance` (`provider/aws/aws.go`). If the runner is killed (Ctrl-C, crash,
OOM, CI timeout) between provision and teardown, the cleanup never runs and we leak
a running RDS instance that keeps billing.

Temporal's **durable execution** closes this gap: orchestration state is persisted
to a database and replayed after a crash, so a deferred teardown is *guaranteed* to
run — even after `kill -9`. That single property is the reason for this work.

---

## Terminology

| Term | What it is | In `dbtest` |
|---|---|---|
| **Workflow** | Orchestration code. Deterministic — it only *decides* the order of steps; it may not do I/O directly. Survives crashes via replay. | One function per scenario, e.g. `CrashRecoveryWorkflow` (replaces the step ordering in `scenarios.go`) |
| **Activity** | A single side-effecting operation: provision, run pgbench, fingerprint. May do I/O, fail, and be retried. | Thin wrappers over the existing step logic (`Provision`, `RunWorkload`, `Fingerprint`, …) |
| **Worker** | A long-running process that hosts your workflow + activity code, polls a task queue, and executes work. The only thing that runs your Go functions. | New `cmd/worker` binary |
| **Starter / Client** | A program that *triggers* one workflow execution and then leaves. Does not run the code. | `cmd/runbenchmark`, after swapping `scenario.Run` for `client.ExecuteWorkflow` |
| **Task Queue** | The named rendezvous between starter and worker. They never talk directly — both reference the same queue name (e.g. `dbtest-tq`). | Wrong name on either side → workflow hangs, never runs |
| **Workflow Execution** | One running instance of a workflow, identified by a Workflow ID. Starting with the same ID is deduplicated. | One scenario run |
| **Event History** | The append-only, durable log of everything that happened in an execution (`ActivityScheduled`, `ActivityCompleted`, …). Replay reads this to skip completed work. | The source of truth that survives a crash |
| **Determinism / Replay** | After a crash, Temporal re-runs the workflow function from the top, feeding completed activities their stored results. The function must make the *same decisions* each time, so no `time.Now()`, `rand`, maps, or I/O inside a workflow. | Why `RunContext` must be split (see Boundary) |
| **Retry Policy** | Per-activity config for automatic retries on failure. | Replaces hand-rolled `WaitForReady` loops |
| **Heartbeat** | A long-running activity periodically reports liveness so Temporal doesn't consider it dead and retry it. | Needed for `aws.waitForEndpoint` (polls up to 15 min) |
| **`defer` (compensation)** | Go `defer` inside a workflow is the durable "cleanup that always runs" — success, failure, or cancellation. | The teardown stack, made crash-proof |
| **Idempotency** | An activity may run more than once (retry/replay), so it must be safe to repeat. | `Deprovision` treats NotFound as success; `Provision` must not create duplicates (see Risks) |
| **Temporal server** | The service holding queues and histories. Does **not** run your code and does **not** know which workflows exist — it routes tasks by queue name only. | `temporal server start-dev` in dev |
| **Namespace** | An isolation boundary within a Temporal server (like a database schema). | Default `default` in dev |

### Mental model

```
STARTER                 SERVER                    WORKER
(cmd/runbenchmark)       (temporal)                (cmd/worker)
  │  "run scenario X"      │  records intent         │  hosts workflow+activity code
  ├───────────────────────►│  on task queue          │  polls the queue
  │                        │────────────────────────►│  runs the workflow
  │                        │◄────────────────────────┤  "execute Provision activity"
  │                        │  stores result durably   │  runs the activity → provider
  │◄───────────────────────┤  workflow completes      │
```

The server is a **blind router** — it stores what should happen and delivers tasks
to whoever is polling. The worker is the **only** place your code runs. The starter
just fires the trigger.

### Airflow analogy (for readers coming from Airflow)

| Airflow | Temporal |
|---|---|
| DAG | Workflow (but imperative Go — `if`/`for` are native, no operator graph) |
| Task / Operator | Activity |
| XCom (push/pull) | Plain return values held in workflow variables |
| `retries=3` on a task | `RetryPolicy` on an activity |
| Worker / Executor | Worker |
| `trigger_rule="all_done"` cleanup task | Go `defer` in the workflow |

---

## Current architecture

```
cmd/runbenchmark → scenario.Run → executeSteps (in-process, RAM cleanup stack)
                                    ├ provision   (Provider.Provision + WaitForReady)
                                    ├ workload    (workload.Run)
                                    ├ save-result (state.SaveBenchmarkResult)
                                    ├ snapshot    (validator.Fingerprint → state DB)
                                    ├ kill-process(FailureInjector.KillProcess)
                                    └ verify      (re-fingerprint, compare vs state DB)

state DB : runs, checkpoints, benchmark_results, fingerprints, clusters
providers: docker, aws (both real)
```

`RunContext` (`scenario/scenario_engine.go`) threads everything through the steps:
config, the live `Provider`, `ClusterInfo`, the `*pgxpool.Pool`, telemetry, and the
run record.

---

## Target architecture

```
cmd/runbenchmark  (STARTER)  → client.ExecuteWorkflow(...)      ← keeps all its flags
cmd/worker        (WORKER)   → registers workflows + activities, holds live deps

temporal/
  workflows.go    one workflow per scenario, sharing Go helpers
  activities.go   Activities{providerFor, statePool, tel} — the boundary

scenario/…        step *logic* moves into activities; ordering moves into workflows
state DB          keeps domain results; Temporal owns orchestration + teardown
```

---

## The workflow / activity boundary

The core rule: **anything with a live connection stays in an activity; only plain
data crosses into the workflow.** `RunContext` splits along that line:

| `RunContext` field | Verdict | Why |
|---|---|---|
| `Cfg Config` | → Workflow input | plain data |
| `Cluster ClusterInfo` | → Workflow state | already primitive (`ID`, `PGTarget`, `Password`) after the PGTarget refactor — serializes cleanly |
| `Provider` | ✗ Activity-only | live client; rebuilt inside activities via `provider.Run(name, tel)` |
| `StatePool *pgxpool.Pool` | ✗ Activity-only | live pool |
| `Tel *telemetry.Telemetry` | ✗ Worker-injected | live exporters; a field on the `Activities` struct |
| `StateRun *state.Run` | ↦ Replaced | the workflow ID is the run identity; typed writes become activities |
| `Result workload.Result` | ⚠ Concrete type needed | an interface can't be deserialized by Temporal (see Risks) |
| `cleanups []func` | ↦ `defer` | the teardown stack, made durable |

> The `PGTarget` refactor did half this work already: `ClusterInfo` is now
> `{ID string, Target PGTarget, Password string}` — all primitives, so it is exactly
> the serializable shape an activity must return.

### Activities (step logic, wrapped)

| Activity | Wraps | Returns |
|---|---|---|
| `Provision` | `Provider.Provision` + `WaitForReady` | `ClusterInfo` |
| `Deprovision` | `Provider.Deprovision` | error (idempotent: NotFound = success) |
| `RunWorkload` | `workload.New(...).Run` | persists to state DB; returns nothing across the boundary |
| `Fingerprint` | `validator.Fingerprint` per table | `map[string]string` (table → digest) |
| `KillProcess` | `FailureInjector.KillProcess` | `ClusterInfo` (refreshed) |
| `SaveResult` | `state.SaveBenchmarkResult` | error |

The provider is reconstructed inside the activity from the provider *name* (a
string) via the existing self-registering registry — the workflow never holds a
live `Provider`.

### Workflows (ordering)

One function per scenario, sharing helpers so the common
provision→workload→teardown skeleton isn't copy-pasted. Example shape:

```go
func CrashRecoveryWorkflow(ctx workflow.Context, cfg Config) error {
    cluster, err := provision(ctx, cfg)        // activity
    if err != nil { return err }
    defer deprovision(ctx, cluster.ID)         // durable teardown, always runs

    runWorkload(ctx, cluster, workload.Warehouse)
    baseline := fingerprint(ctx, cluster, durabilityTables) // held in workflow memory
    cluster = killProcess(ctx, cluster)
    after := fingerprint(ctx, cluster, durabilityTables)
    return compare(baseline, after)            // no state-DB round-trip
}
```

We use a single `defer` (not a Saga helper): each scenario provisions exactly one
cluster, so `defer` placed right after provision succeeds is the complete, correct
form. A Saga is only warranted if a scenario ever provisions a variable number of
resources.

---

## Phased plan

**Phase 0 — Infra & deps**
Add `go.temporal.io/sdk` to the module. Dev server via `temporal server start-dev`.
New `temporal/` package and `cmd/worker/`.

**Phase 1 — Carve the activity boundary**
Build `Activities{providerFor, statePool, tel}` wrapping the step logic above. No
behaviour change — just relocate side effects behind activity methods.

**Phase 2 — First two workflows**
1. `ProvisionTeardownWorkflow` — provision → `defer` deprovision. Smallest end-to-end
   proof; run against **docker** (zero cost). Kill the worker mid-run; watch teardown
   still fire.
2. `CrashRecoveryWorkflow` — the most durability-flavoured scenario.

**Phase 3 — Worker + starter swap**
`cmd/worker` registers all scenario workflows and holds live deps. `cmd/runbenchmark`
keeps its flags; only its tail changes from `scenario.Run(...)` to
`client.ExecuteWorkflow(...)`.

**Phase 4 — Reconcile with the state DB**

| Concern | Old owner | New owner |
|---|---|---|
| Run lifecycle / ordering | `scenario.Run` + `runs` table | Temporal (workflow = run); keep a `runs` row keyed by workflow ID for SQL |
| Guaranteed teardown | RAM cleanup stack | Temporal `defer` |
| `clusters` table + orphan sweep | state DB | keep as audit/reconciliation (belt-and-suspenders) |
| Benchmark results / fingerprints | state DB | stays — real domain data |
| Verify baseline | state-DB round-trip | workflow memory |

**Phase 5 — Retire the old path**
Keep `scenario.Run` as a non-durable fast path behind a `-durable` flag; delete it
once the Temporal path is trusted.

---

## Risks & Temporal-specific decisions

1. **Provision idempotency (sharp edge).** Temporal retries activities on timeout.
   `aws.Provision` generates its instance ID with `uuid.NewString()` *inside* the
   activity (`provider/aws/aws.go`). If the activity times out after
   `CreateDBInstance` succeeds, a retry creates a **second** RDS instance. Fix:
   generate the ID in the workflow (`workflow.SideEffect`) and pass it in, so a retry
   reuses the same ID and the create collides harmlessly.

2. **Leak on provision failure (pre-existing bug, still relevant).** `aws.Provision`
   creates the instance, then waits; if the wait fails it returns without deleting
   the created instance. Under Temporal the workflow's `defer` only covers a
   *successful* `Provision`, so this internal leak must still be fixed in `aws.go`
   (defer-delete on internal failure).

3. **Long provisions need heartbeats + generous timeouts.** `aws.waitForEndpoint`
   polls up to 15 min. The activity's `StartToCloseTimeout` must fit that, and it
   should heartbeat, or Temporal will retry a still-running provision (compounding
   risk #1).

4. **`workload.Result` is an interface.** Temporal can't deserialize an interface
   without a concrete target. `RunWorkload` should persist its result to the state DB
   inside the activity and return nothing across the boundary.

5. **State-DB vs Temporal ownership.** Don't double-own the run lifecycle. Temporal
   owns orchestration; the state DB keeps queryable domain results. The `runs` row
   stays for SQL reporting, keyed by the workflow ID.

---

## First step

Port `ProvisionTeardownWorkflow` into the `dbtest` module against the **docker**
provider. It exercises the entire stack — worker, activity boundary, durable
teardown — at zero cloud cost. Every other scenario is a repeat of that pattern.
