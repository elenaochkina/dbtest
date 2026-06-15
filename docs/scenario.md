# dbtest — Scenario Package Design

> **Status:** the engine and several steps are now built (the old `baseScenario` is gone).
> The sections below are the *original* target design; some specifics have since changed in
> implementation (one parameterised workload step instead of per-primitive steps, descriptive
> scenario names instead of A/B/C, a forced-restart durability scenario). See
> **[Implementation status (as built)](#implementation-status-as-built--2026-06-15)** at the
> end for what actually exists today and where it diverged.

## The whole thing in one sentence

> A **scenario** is an ordered list of **steps**. A **step** is one action that reads and
> writes a shared **run context**. A runner executes the list in order and guarantees
> teardown.

Three nouns — **Scenario** (the list), **Step** (one action), **RunContext** (the shared
state) — are the entire architecture. Everything else is detail.

The caller picks a scenario by name (`-scenario A`); the scenario decides which steps run
and in what order:

```
A:  provision → benchmark → save-result
B:  provision → warehouse → benchmark
C:  provision → warehouse → scale → benchmark
```

Teardown (`deprovision`) is **not listed** in the script — the runner guarantees it (see
[Teardown](#teardown)), so it can never be forgotten or skipped on failure.

---

## Step vs. workload vs. provider

This is the distinction that makes the rest obvious. The project already has *primitives*:

- **`provider`** — control-plane primitive: *how to get a database*
  (`Provision`, `WaitForReady`, `Deprovision`).
- **`workload`** — data-plane primitive: *what to do to a database* (warehouse, pgbench).
  Knows nothing about clusters.

A **step is not a new kind of primitive.** It is a thin **adapter** that turns one
primitive call into "one line of the script," behind a single uniform signature so all
steps can sit in the same list:

| Step | Adapts | Reads / writes RunContext |
|---|---|---|
| `provision` | `provider.Provision` + `WaitForReady` | writes `Cluster` / DSN |
| `warehouse` | `workload.Run` (warehouse) | reads DSN |
| `benchmark` | pgbench | reads DSN, writes `Result` |
| `scale` | `provider.Scale` (**new — see Gaps**) | reads `Cluster` |
| `save-result` | `state` result writer | reads `Result` |
| `deprovision` | `provider.Deprovision` | reads `Cluster` (runs as cleanup, not a listed step) |

The provider/workload packages are the **muscle**; steps are the **glue** that sequences
them. Each step is tiny — a few lines that adapt a primitive and move data through the
shared context. This flattening is why a control-plane action (`provision`) and a
data-plane action (`warehouse`) can live in the same list.

---

## RunContext — the shared bag

Steps pass data forward through one struct threaded into every step. `provision` produces
a DSN that `warehouse` and `benchmark` consume; `benchmark` produces a result that
`save-result` consumes.

```go
type RunContext struct {
    Cfg       Config
    Provider  provider.Provider
    Cluster   provider.ClusterInfo   // provision writes; later steps read
    StatePool *pgxpool.Pool          // optional — save-result skipped when nil
    Tel       *telemetry.Telemetry
    Result    *pgbench.Result        // benchmark writes; save-result reads

    cleanups  []func(context.Context) // teardown stack — see Teardown
}
```

---

## Step interface

```go
type Step interface {
    Name() string
    Run(ctx context.Context, rc *RunContext) error
}
```

A concrete step is small — it just adapts a primitive:

```go
type warehouseStep struct{}

func (warehouseStep) Name() string { return "warehouse" }

func (warehouseStep) Run(ctx context.Context, rc *RunContext) error {
    w, err := workload.New(workload.Warehouse, toWorkloadConfig(rc.Cfg))
    if err != nil {
        return err
    }
    return w.Run(ctx, rc.Cluster.DSN, rc.Tel)   // reads the DSN provision put there
}
```

---

## Scenario registry

Scenarios self-register by name — the same pattern `provider` and `workload` already use.
Adding a scenario is a one-line registration; the engine never changes.

```go
type Scenario struct {
    name  string
    steps []Step
}

var registry = map[string]Scenario{}

func Register(name string, steps ...Step) { registry[name] = Scenario{name, steps} }

func init() {
    Register("A", provisionStep{}, benchmarkStep{}, saveResultStep{})
    Register("B", provisionStep{}, warehouseStep{}, benchmarkStep{})
    Register("C", provisionStep{}, warehouseStep{}, scaleStep{}, benchmarkStep{})
}
```

---

## The runner

The only "engine" code, and it is small. It runs the steps in order and logs each one:

```go
func (s Scenario) run(ctx context.Context, rc *RunContext) error {
    for _, step := range s.steps {
        rc.Tel.Logger.Info("step start", "scenario", s.name, "step", step.Name())
        if err := step.Run(ctx, rc); err != nil {
            return fmt.Errorf("step %q: %w", step.Name(), err)
        }
    }
    return nil
}
```

---

## Teardown

Without an external workflow engine, the cleanup guarantee is a **cleanup stack** — the
in-process equivalent of a Saga, implemented with `defer`. If `benchmark` fails, a
`deprovision` listed *after* it would never run and leak a paying cluster. Instead:

1. `provision`, after a successful `Provision`, **registers its own undo** on the stack.
2. The top-level entry point **always drains the stack on exit**, in reverse order — on
   success, on a failed step, or on panic-unwind.

```go
// provisionStep.Run, after Provision succeeds:
rc.cleanups = append(rc.cleanups, func(ctx context.Context) {
    rc.Provider.Deprovision(ctx, rc.Cluster.ID)
})

// top-level entry point:
func Run(ctx context.Context, name string, rc *RunContext) (err error) {
    sc, ok := registry[name]
    if !ok {
        return fmt.Errorf("unknown scenario %q", name)
    }
    defer func() {
        for i := len(rc.cleanups) - 1; i >= 0; i-- { // reverse order
            rc.cleanups[i](context.Background())
        }
    }()
    return sc.run(ctx, rc)
}
```

This guarantees the cluster is torn down no matter where the script aborts. It does **not**
survive a process crash — that is an accepted limitation at this stage. Each registered
cleanup should be idempotent so a future retry can't double-deprovision.

---

## How this maps to existing code

| New thing | Built from |
|---|---|
| `provisionStep` / `deprovisionStep` (cleanup) | the provision/deprovision logic in today's `base.go` |
| `warehouseStep` / `benchmarkStep` | wrap `workload.New(...).Run(...)` — no change to `workload` |
| `saveResultStep` | `state/benchmark.go`'s result writer |
| `scaleStep` | **new `provider.Scale`** — deferred; A and B don't use it |
| runner + registry + teardown | new, ~40 lines total |

`base.go` is **dissolved into steps**: its provision/wait/deprovision logic moves into the
provision step and the cleanup stack, and the hardcoded "workload in the middle" becomes
whatever steps the chosen scenario lists.

---

## CLI usage

```bash
# pick a scenario by name; the scenario decides the step sequence
go run ./cmd/runbenchmark/ -scenario A -provider docker

# scenario B with custom warehouse seed and pgbench load
go run ./cmd/runbenchmark/ -scenario B -seed 123 -warehouses 10 -clients 8 -duration 30s

# scenario C exercises the scale step
go run ./cmd/runbenchmark/ -scenario C -scale 4
```

`-scenario` replaces `-workload` as the *selection* flag — it chooses the script. The
parameter flags (`-seed`, `-warehouses`, `-scale`, `-clients`, `-duration`) remain, since
steps read them via `Config` → `RunContext`.

---

## Gaps / not yet built

- **`provider.Scale` capability.** The `Provider` interface has `Provision`,
  `WaitForReady`, `Deprovision` — no `Scale`. Scenario C's `scale` step needs a new method
  (e.g. `Scale(ctx, cluster, factor)`) plus a docker implementation. A and B work without it.
- **Step extraction.** The provision/deprovision/workload logic in today's `base.go` needs
  to be relocated into discrete steps.
- **Crash durability.** The cleanup stack survives a failed step, not a crashed process.
  Out of scope for this stage.

---

## Package dependencies

```
scenario/   ← imports provider/, workload/, state/, telemetry/
workload/   ← imports benchmark/, pgadapter/, pgbench/, validator/, telemetry/
provider/   ← imports telemetry/ only
state/      ← imports provider/, telemetry/
```

No cycles. `scenario` sits at the top of the stack — only `main.go` and tests import it.
</content>


I've re-read the Teardown section (docs/scenario.md:149-182). Here's what each clause of that statement means, mapped to the design:

"the defer-based cleanup stack (Saga-without-Temporal)"

The doc frames teardown as a cleanup stack — cleanups []func(context.Context) on the RunContext (scenario.md:74). It explicitly calls this "the in-process equivalent of a Saga" (scenario.md:151).

A Saga is a distributed-systems pattern: each forward action registers a compensating action that undoes it, and on failure you run the compensations in reverse. Normally you'd reach for a workflow engine like Temporal to durably orchestrate that. The doc's whole point is that it gets the shape of a Saga "without an external workflow engine" (scenario.md:150) — using nothing but Go's defer. Hence "Saga-without-Temporal": same compensation-in-reverse semantics, but it lives entirely in one process instead of a durable orchestrator.

"provision registers its own undo"

Point 1 of the section (scenario.md:156-157) and the code at scenario.md:160-163: the moment provision succeeds, it appends its own compensating closure (Deprovision(rc.Cluster.ID)) onto the stack. This is the key Saga property — the step that creates the resource is the same step that knows how to destroy it, so cleanup registration is colocated with the action that made cleanup necessary. Nothing registers a teardown for a resource that was never created.

"the entry point always drains the stack in reverse"

Point 2 (scenario.md:158-159) and the Run function (scenario.md:166-177). The top-level Run wraps the whole scenario in a defer that walks the stack from top to bottom (for i := len(rc.cleanups)-1; i >= 0; i--). Because it's a defer, it fires on every exit path the section lists (scenario.md:158-159): clean success, a failed step returning an error, or a panic unwinding the stack. This is what makes the doc's opening claim true — teardown is "not listed in the script" (scenario.md:25) precisely because the runner guarantees it, so it "can never be forgotten or skipped on failure" (scenario.md:26).

Reverse order matters for the same reason it does in a Saga: later resources may depend on earlier ones, so you unwind newest-first. The motivating failure is spelled out at scenario.md:152-153 — a deprovision listed after benchmark would be skipped if benchmark failed, leaking a paying cluster. The stack removes that whole class of bug.

"Notes idempotency and the explicit no-crash-durability caveat"

The closing paragraph (scenario.md:180-182) carries two honest caveats:

- Idempotency — "Each registered cleanup should be idempotent so a future retry can't double-deprovision" (scenario.md:182). Because a retry might run cleanup again, Deprovision must tolerate being called on an already-gone cluster.
- No crash durability — "It does not survive a process crash — that is an accepted limitation at this stage" (scenario.md:180-181), echoed in Gaps at scenario.md:228-229. This is the precise boundary of the "without Temporal" tradeoff: defer covers errors and panics within the process, but if the process is kill -9'd, the stack dies with it and the cluster leaks. A real Temporal-backed Saga would survive that; this one deliberately doesn't, and the doc says so rather than pretending otherwise.

So the statement is a faithful one-line compression: it names the mechanism (defer stack), the pattern it imitates (Saga), the two halves of how it works (self-registered undo + guaranteed reverse drain), and the two caveats the doc is careful to flag (idempotency, crash durability).

---

## Design notes: the workload layer and result reporting

These decisions were settled while reviewing the layering before implementation. They
refine — not replace — the step model above.

### Keep `workload` as a layer; give `Run` a result

`workload` stays a distinct layer between the primitives and the scenario steps. It keeps
its existing shape — the `Workload` interface (`Name()` + `Run()`) and the self-registration
pattern — with one change: **`Run` now returns a result**, and each workload delegates to its
respective primitive package and passes that result up.

```go
type Workload interface {
    Name() string
    Run(ctx context.Context, dsn string, tel *telemetry.Telemetry) (Result, error)
}
```

This is the current working choice, not a settled one — under production rules it stands only
if the additional-workload roadmap justifies the abstraction (see "Alternative considered:
dissolve the `workload` layer" below). The alternatives:

- **Dissolve `workload` into steps** — the leading alternative; deferred to mentor review, not
  rejected on cost (the refactor is mechanical). See the full write-up below.
- **Keep `workload`, but route persistence through it** — rejected on merits; see "observability
  vs. persistence" below.

The motivating problem: today `Workload.Run` returns only `error`, so the pgbench wrapper
**discards** the `pgbench.Result` it computes. The instant a step needs that result (for
`save-result`), it would have to bypass `workload` entirely. Returning a result from `Run`
closes that gap and keeps `workload` on the path.

### `Result` is an agnostic interface, not a struct

`Result` must **not** be a struct with per-workload fields (`Pgbench *pgbench.Result`,
`Replication *...`, …) — that is the fat-struct trap: every new workload forces an edit to a
shared type that ends up knowing about all of them. Instead `Result` is a small, domain-neutral
interface that each concrete result type satisfies **structurally** (no import cycle; the
primitive packages do not import `workload`):

```go
// in workload
type Result interface {
    Metrics() map[string]float64
}
```

Adding a workload later (e.g. replication) means defining a new result type with its own
`Metrics()` — the `workload` package never changes.

The `Metrics() map[string]float64` contract is the industry lingua franca for this: it mirrors
Go's own benchmark framework (`testing.B.ReportMetric(value, unit)`) and the named-numeric model
shared by Prometheus / OpenTelemetry / StatsD. Consumers (telemetry, logs, dashboards) iterate
name→value uniformly without knowing the concrete type.

Two implementations satisfy it:

```go
// pgbench/result.go — emitting view over the typed Result
func (r Result) Metrics() map[string]float64 {
    return map[string]float64{
        "tps":               r.TPS,
        "latency_avg_ms":    r.LatencyAvgMs,
        "latency_stddev_ms": r.LatencyStddevMs,
    }
}

// validator/validator.go — warehouse reports real numbers, not just pass/fail
func (c Checksum) Metrics() map[string]float64 {
    return map[string]float64{
        "row_count": float64(c.RowCount),
        "stock_sum": float64(c.StockSum),
    }
}
```

The warehouse workload returns its post-cycle `validator.Checksum` instead of discarding it.
(The warehouse "numbers" live in `validator`, not `benchmark` — `benchmark` holds only table
ops and `RunOrderCycle`.)

### Observability view vs. persistence contract

The key separation, and the part most worth defending in review: **the metrics map is the
observability contract; the typed structs remain the persistence contract.** They are kept
apart on purpose.

- `Metrics() map[string]float64` is great for *emitting* — logs, metrics, dashboards — and is
  uniform across all workloads.
- It is *lossy for typed persistence*: the `benchmark_results` table (`state/benchmark.go`) has
  real typed columns (`tps`, `latency_avg_ms`, `scale_factor`, `clients`, `duration_seconds`),
  and a `map[string]float64` flattens those into stringly-named floats and drops
  `Duration`'s type.

So persistence stays typed: `state.SaveBenchmarkResult` keeps taking a concrete `pgbench.Result`
and mapping fields → columns. The database is **not** routed through the lossy map. The
alternative — a fully generic `(run_id, metric_name, value)` key/value metrics table — buys
flexibility at the cost of typed columns, constraints, and easy SQL querying; it is what large
observability backends do and is overkill here.

### Implementation order (agreed)

1. Add the `Result` interface to `workload`.
2. Add `Metrics()` to `pgbench.Result` and `validator.Checksum`.
3. Change `Workload.Run` to return `(Result, error)`; update the one caller in `base.go`.

The scenario-side wiring (steps consuming the result for `save-result`) follows after, building
bottom-up.

### Open / deferred

- **Dissolving `workload` into steps** — kept as a layer for now; flagged for mentor review.
  See the full write-up below.
- **Warehouse delta vs. checksum** — warehouse currently returns the post-cycle checksum
  (`row_count`, `stock_sum`); whether it should instead surface the stock *delta* is undecided.
- **Reconnect + heartbeat + cluster-tracking** (`base.go`) — the doc's simplified provision step
  drops this; whether to preserve, fold into the teardown stack, or drop is still open.

### Alternative considered: dissolve the `workload` layer

The chosen design keeps `workload` as a layer (interface + registry, `Run` returning a
`Result`). The main alternative is to **remove the `workload` layer entirely** and have each
scenario step call its data-plane primitive directly. Both are valid; this records the
alternative so it can be weighed in review.

**The core idea.** The data-plane logic does not disappear — it *moves*. "Call each action
directly" means the *step* becomes the adapter over the primitive, with no `Workload` interface
in between. This is already what the step table implies for `benchmark` (adapts `pgbench`) and
`save-result` (adapts `state`); dissolving just makes `warehouse` symmetric with them.

**What it looks like concretely.**

- `benchmarkStep` → calls `pgbench.RunLocal(...)` directly. Trivial: pgbench is already a
  one-call primitive returning a `Result`.
- `warehouseStep` → the warehouse cycle currently in `workload/warehouse.go` (drop/create/seed
  tables, `RunOrderCycle`, before/after checksums, delta check) has to live somewhere. It
  becomes a **plain function in its own small package**, e.g.:

  ```go
  // package warehouse
  func Run(ctx context.Context, pool *pgxpool.Pool, cfg Config, tel *telemetry.Telemetry) (validator.Checksum, error)
  ```

  The step calls it directly and stores the returned `Checksum` on the `RunContext`.

So this is less "delete a layer" and more **demote the warehouse orchestration from an
interface-implementing type to a plain function, and let `scenario` be the only registry**.

**What gets deleted.** `workload/workload.go` (the `Workload` interface, `New`, `allWorkload`,
the registry), `workload/pgbench.go`, `workload/warehouse.go` — replaced by the `warehouse`
function above plus direct `pgbench.RunLocal` calls. The `Result` interface is still useful
and would move to wherever results are reported (or be dropped if steps just read typed
results off the `RunContext`).

**Why dissolve (the case for).** Once scenario steps do the composing and selecting, the
`workload` registry duplicates the scenario registry one level down, and step ordering subsumes
`workload.All`. The `Workload` interface becomes a second registry doing what the scenario
registry already does — the original "too many layers" smell. Idiomatic Go leans this way:
*accept interfaces, return structs* + YAGNI — don't add an interface until a second
implementation **and** a real need for runtime polymorphism exist. With a small fixed set
(warehouse, pgbench) composed at the scenario layer, plain functions are the default and the
interface is speculative generality.

**Why keep (the case against dissolving).** The `Workload` interface + registry buys uniform
polymorphism (a `[]Workload`, select-by-name, the `All` composition) and self-registration (a
new workload plugs in via `init()` with no central switch). This earns its place **only if a
real, near-term roadmap of additional workloads exists** (e.g. replication) that would be
selected or composed uniformly — i.e. the abstraction pays for a need that is actually coming,
not a hypothetical one. Consistency with the `provider` and `scenario` registries is a minor
maintainability plus, but is not on its own sufficient justification under production rules.

**When each wins.**

| Keep `workload` interface/registry | Dissolve — call primitives directly |
|---|---|
| Many interchangeable impls selected/composed *dynamically* | Small fixed set; composition lives in scenario |
| You iterate a `[]Workload` uniformly (plugin system) | Each step adapts a specific primitive anyway |
| A concrete roadmap of more workloads needs uniform handling | One registry (scenario) is enough for the current set |

**Decision (production lens): keep the `workload` layer.** The open question was whether the
additional-workload roadmap is real and near-term enough to justify the abstraction now. It is:
more workloads are planned for the next stages, selected and composed by name through the same
interface. With a concrete, incoming set of distinct workloads — not a hypothetical one — the
`Workload` interface + registry pays for a need that is actually arriving. YAGNI no longer
points at dissolving: the further implementations are on the roadmap, so the abstraction is
justified rather than speculative.

---

## Implementation status (as built — 2026-06-15)

This section is the source of truth for what exists in code; the earlier sections are the
original design and have partly diverged.

### Engine (`scenario/scenario_engine.go`)

- **`RunContext`** — `Cfg`, `Provider` (resolved by the caller), `Cluster`, `StatePool`, `Tel`,
  `StateRun *state.Run`, `Result workload.Result`, and the private `cleanups` teardown stack.
- **`Step`** (`Name()` + `Run(ctx, *RunContext) error`), **`Scenario`**, the `registry`,
  `Register`, `Names`, the `run` loop, and **`Run(ctx, name, rc)`**.
- **`Run` owns two cross-cutting guarantees:**
  1. **State run lifecycle** — when `StatePool != nil`, it calls `state.StartRun(scenario, seed,
     provider)` on entry (the `runs` row / shared `run_id`) and `rc.StateRun.End(passed = err == nil)`
     in teardown.
  2. **Teardown stack** — drains `cleanups` in reverse on every exit (success, failed step,
     panic), then ends the run. Uses `context.Background()` so cleanup runs even if the caller's
     ctx is dead. Does not survive a process crash.

### Steps (`scenario/steps.go`)

| Step | Adapts | Notes |
|---|---|---|
| `provisionStep` | `provider.Provision` + `WaitForReady` | registers deprovision undo *before* WaitForReady |
| `workloadStep{name}` | `workload.New(name).Run` | **one parameterised step** for warehouse *and* pgbench; writes `rc.Result` |
| `snapshotStep{label, tables}` | `validator.Fingerprint` + `state` | persists a baseline digest per table; **requires a state DB** |
| `verifyStep{label, baseline, tables}` | `validator.Fingerprint` + `state` | re-fingerprints, persists, asserts `== baseline` read back from the DB |
| `restartStep` | `provider.Restarter` (type assertion) | forced restart, updates `rc.Cluster`, re-`WaitForReady` |

### Registered scenarios (`scenario/scenarios.go`)

- `warehouse` — provision → warehouse
- `benchmark` — provision → pgbench
- `all` — provision → warehouse → pgbench
- `restart` — provision → warehouse → `snapshot(before_restart)` → restart → `verify(after_restart)`,
  over `durabilityTables = ["warehouse", "orders"]`. **Requires `STATE_DSN`.**

### Supporting changes in other packages

- **`workload`** — `Run` now returns `(Result, error)`; `Result` is the agnostic
  `Metrics() map[string]float64` interface (`pgbench.Result` and `validator.Checksum` implement it).
  The dead `All`/`allWorkload` composition was removed (scenario composes now).
- **`validator.Fingerprint(table)`** — `md5(string_agg(row::text ORDER BY row::text))`, a content
  hash strong enough to prove byte-for-byte survival (vs `Checksum`, which stays for the −10 delta).
- **`state`** — new `fingerprints` table; `Run.SaveFingerprint` / `Run.GetFingerprint`. The
  `StartRun`/`End` lifecycle is now driven by the engine, not only the test.
- **`provider.Restarter`** capability interface; **`dockerProvider.Restart`** does a *forced*
  restart — `SIGKILL` → wait-for-exit → start → re-inspect DSN — so the DB comes back through WAL
  crash recovery. Graceful restart was deliberately rejected (it always persists, so the check
  would be meaningless). `hostPort`/`dsnForPort` factored out and shared with `Provision`.

### The restart durability flow

```
provision → warehouse → snapshot(before_restart) → restart(SIGKILL+recover) → verify(after == before)
```

`snapshot` and `verify` fingerprint `warehouse` + `orders`, persisting both digests under the same
`run_id`. The assertion is **DB-backed**: `verify` reads the baseline back from `state`, so it is
not lost if a process dies mid-run. A mismatch is a real durability finding (e.g. `fsync=off`),
not a flake. Run it:

```bash
STATE_DSN=postgres://… go run ./cmd/runbenchmark/ -scenario restart -provider docker -warehouses 5
```

### Divergences from the original design above

- **Per-primitive steps → one `workloadStep`.** Because benchmark routes *through* `workload`
  (the keep-`workload` decision), warehouse and pgbench are both "run a workload," so a single
  parameterised step replaces the separate `warehouse`/`benchmark` steps the step table lists.
- **Scenarios A/B/C → descriptive names** (`warehouse`/`benchmark`/`all`/`restart`).
- **`RunContext.Result` is `workload.Result`** (interface), not `*pgbench.Result`.
- **No reconnect/heartbeat/state-cluster-tracking** — the old `base.go` machinery was dropped;
  teardown still guarantees deprovision, but cross-process orphan recovery is gone.

### Still pending

- **`save-result` step** — benchmark results → `benchmark_results` via `rc.StateRun`. (`Result`
  already flows to `rc.Result`; the persisting step is not built. Fingerprints are persisted
  directly by snapshot/verify, not via a generic save-result.)
- **`provider.Scaler`** capability + a `scale` step (the original Scenario C).
- **Integration test** for the `restart` scenario (gated like the docker test).
