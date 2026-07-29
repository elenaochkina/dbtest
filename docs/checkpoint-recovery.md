# Plan: Checkpoint vs. Recovery Time
staying Measure how long Postgres takes to come back after an ungraceful kill, and how
that time depends on **whether a checkpoint had just happened**. Docker-local
first; the same workflow should later point at RDS unchanged.

Status: design. No code yet.

`CrashRecoveryWorkflow` is left untouched — this is a new, separate workflow.

---

## 1. Hypothesis

Postgres crash recovery replays WAL from the **redo point** of the last completed
checkpoint to the end of the WAL. So:

| Arm | WAL to replay | Expected recovery |
|---|---|---|
| **A — kill right after `CHECKPOINT`** | ~nothing | fast; possibly `redo is not required` |
| **B — kill with WAL outstanding** | one load-phase worth | slower, proportional to WAL volume |

The experiment is only meaningful if **WAL-since-checkpoint is the only thing
that differs between the arms.** Everything in §3 exists to make that true.

## 2. Two findings from the current code

**2.1 — There is no pgbench container yet.** PR #17 (`pgbench-container`) merged
only `docs/fargate-benchmark.md`. Today pgbench runs as a local binary via
`exec.Command` in `pgbench/runner.go:26`, which also means the worker host must
have `pgbench` on its `PATH`.

**Decided:** the load generator becomes its own binary (`cmd/bench`) in its own
image, used **locally as well as** in the cloud — see §5. Running stock
`postgres:16` with an overridden entrypoint would work for this experiment (that
image already ships `/usr/bin/pgbench` — verified, `16.14`), but it is a dead end:
pgbench cannot reconnect, cannot report structured results, and cannot carry the
acked-commit bookkeeping the durability work needs. One artifact, exercised the
same way in both environments, is worth the build step.

**2.2 — The existing `KillProcess` → `WaitForReady` split cannot measure this.**
They are two activities. Between the SIGKILL and the first probe you pay a full
Temporal round-trip: activity completion → workflow task → activity dispatch.
That is plausibly 50–500 ms locally — the same order of magnitude as the effect
we're trying to measure. The result would be mostly Temporal.

> **Constraint: kill and probe must happen inside one activity.** Same shape as
> the fingerprint-gap problem in the PR #16 review, applied to timing instead of
> durability: anything that must be observed *at* an instant cannot be split
> across an activity boundary.

## 3. Experimental controls

### 3.1 Pin checkpointing (mandatory)

Provision the container with:

```
-c checkpoint_timeout=1h
-c max_wal_size=64GB
```

With the defaults (`5min` / `1GB`), pgbench triggers checkpoints on its own
schedule via `max_wal_size`. Arm B then silently becomes "killed at some
uncontrolled point after some checkpoint we didn't know about," and the
comparison measures nothing. Pinning these makes **the workflow the only source
of checkpoints.**

### 3.2 Reset the WAL baseline after `pgbench -i` (mandatory)

`pgbench -i -s N` writes a lot of WAL. With checkpointing pinned off (§3.1) that
WAL is *never* flushed away on its own. So both arms must issue a `CHECKPOINT`
**after init, before the load phase starts.**

Without this, arm B's WAL backlog is `init + load` instead of `load`, and the two
arms differ by more than the one variable we're testing.

### 3.3 Identical load in both arms

Same duration, same clients, same scale factor. The arms differ by exactly one
statement: whether `CHECKPOINT` runs immediately before the kill.

### 3.4 Record the independent variable

Immediately before the SIGKILL, in the same activity:

```sql
SELECT pg_current_wal_lsn() - (SELECT redo_lsn FROM pg_control_checkpoint());
```

This is **WAL bytes since the last checkpoint's redo point** — the actual input
to recovery cost. Recording it is what makes the comparison defensible rather
than anecdotal: it lets you plot recovery time *against* WAL volume instead of
just asserting "arm B was slower."

It also catches a broken run. If arm B reports ~0 WAL bytes, the load never
landed and the result is garbage — that should hard-fail, not quietly report a
fast recovery.

## 4. Metrics

Three numbers, answering different questions. All are recorded per run.

| Metric | Source | What it means |
|---|---|---|
| `downtime_ms` | wall clock, SIGKILL → first successful `SELECT 1` | End-to-end. Includes container restart overhead. What a user feels. |
| `redo_elapsed_ms` | parsed from the server log | WAL replay in isolation, free of restart noise. **The number that should differ between arms.** |
| `wal_bytes` | §3.4 | The input that explains the output. |

The log line, emitted at `LOG` severity (which ranks *above* `WARNING` in
`log_min_messages` ordering, so it appears with default settings):

```
LOG:  redo done at 0/1A2B3C4D system usage: CPU: user: 0.12 s, system: 0.05 s, elapsed: 1.23 s
```

In arm A expect instead:

```
LOG:  redo is not required
```

→ `redo_elapsed_ms` is NULL. **That NULL is a result, not a missing value.**

Read via the Docker logs API with `Since: t0`, so we only parse this restart's
output.

> **Open unknown, to be answered by data rather than argued:** Postgres performs
> an end-of-recovery checkpoint, and whether it completes before or after the
> postmaster starts accepting connections is version- and path-dependent. Rather
> than reason about it, we measure both `downtime_ms` and `redo_elapsed_ms` and
> see how the gap behaves across arms.

## 5. Architecture

Three layers, each ignorant of the others:

```
worker/activity   records metrics, persists results   ← long-lived, scrapeable
      │


      │                                                  not pgbench
cmd/bench         runs a workload, prints the result   ← knows workloads,
                                                         not where it runs
```

`cmd/bench` never learns where it is running; `harness` never learns what it
started. That is what makes Docker→ECS swappable without touching pgbench, and
pgbench→ledger swappable without touching ECS.

| Concern | Name |
|---|---|
| Binary that runs workloads | `cmd/bench` → image `dbtest/bench:dev` |
| Package that launches it | `harness/` (+ `harness/docker`, later `harness/ecs`) |
| Workflow | `RecoveryWorkflow` |
| Results table | `recovery_results` |

### 5.1 The workload binary: `cmd/bench`

A standalone binary in a standalone image. Locally it runs as a Docker container
next to the database container; on Fargate the identical image runs as an ECS
task. The only thing that differs is who launches it and where its logs are read.

**Why a binary and not just `pgbench`.** pgbench is a one-shot CLI: it aborts its
clients when the connection drops, writes only human-readable text to stdout, and
holds no state you can query. The wrapper is what buys reconnect, a structured
result, signal handling, and — later — the acked-commit watermark. Those are what
make it a *service* rather than a command.

**It is a thin runner over the existing `workload` registry** — not a new
abstraction. `workload.New(name, cfg)` already exists (`workload/workload.go:63`)
with `pgbench` and `warehouse` registered via `init()`. So `main()` is: parse
flags → resolve the workload → `Run(ctx, dsn, tel)` → print the result.

```
bench -workload pgbench -dsn … -scale 10 -clients 8 -duration 60s
```

| Workload | Status |
|---|---|
| `pgbench` | build now — shells out to `pgbench`, reuses the parsing in `pgbench/runner.go` |
| `ledger` | later — native `pgx` writers with the per-writer acked-sequence watermark from the PR #16 review |

Wrapping pgbench rather than reimplementing it keeps TPC-B — a known, externally
comparable workload — and reuses `parsePgbenchOutput`, which already handles the
TPS line changing wording across Postgres versions. When the durability work needs
the watermark, `ledger` registers alongside it and pgbench drops out. **The
container boundary does not change.**

**Two changes needed in `pgbench/`:**

1. Rename `RunLocal` → `Run`. "Local" becomes false the moment this executes on
   Fargate.
2. Delete the `Initialize` call at `runner.go:40`. `Initialize` is already a
   separate exported function (`runner.go:25`), so callers compose the two — which
   is what lets the workflow put the reset `CHECKPOINT` between them (§3.2).

Change 2 implies the `workload.Workload` interface needs an optional init
capability, mirroring how `provider.FailureInjector` is already an optional
capability:

```go
// Initializer is an optional workload capability: workloads needing setup
// before the measured phase implement it.
type Initializer interface {
    Initialize(ctx context.Context, dsn string, tel *telemetry.Telemetry) error
}
```

`bench -init` runs only that and exits. The default invocation runs the measured
phase until `-duration` elapses or SIGTERM arrives.

The reset `CHECKPOINT` stays in the **workflow**, between those two invocations,
rather than inside `bench` — it is experiment-specific, it is not timing-sensitive
(unlike the pre-kill checkpoint), and keeping it out leaves `bench` generic.

**Result protocol.** One JSON line on stdout behind a sentinel:

```
DBTEST_RESULT {"tps":2871.4,"latency_avg_ms":1.39,"aborted":false,...}
```

Read locally through the Docker logs API — which we already need for the redo line
(§4) — and on Fargate through CloudWatch. Chosen over an HTTP endpoint because it
needs **no inbound network path to the container**: a Fargate task in a private
subnet cannot be polled from a laptop without a load balancer.

> **Metrics move up a layer.** `RunLocal` currently sets Prometheus gauges
> (`runner.go:62-73`). Those are *scraped*, which only works for a long-lived
> process — a container that runs 60 s and exits will never be polled, and on
> Fargate it has no reachable address at all. So `bench` **pushes** its result out
> on stdout, the harness carries it back, and the **worker** sets the gauges. Same
> metrics, recorded by the process that can actually serve them.

**Exit semantics.** Losing the connection mid-run is the expected outcome in a
crash test, not a failure. `bench` emits what it has with `"aborted": true` and
exits 0. A workflow that wants to treat that as fatal decides so itself.

> Note the tension this exposes: `workload.Run` returns `(Result, error)`, and on
> a lost connection `pgbench.RunLocal` returns `Result{}, err` — everything is
> discarded. Harmless here (§5.6 — we ignore bench's output entirely), but the
> `ledger` workload will need partial results to be expressible.

**Image.** Base on `postgres:16` so `pgbench` is present and version-matched with
the server under test, and because that image is already pulled:

```dockerfile
FROM golang:1.25 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/bench ./cmd/bench

FROM postgres:16
COPY --from=build /out/bench /usr/local/bin/bench
ENTRYPOINT ["bench"]
```

Overriding `ENTRYPOINT` bypasses the base image's database-init script — we want
the pgbench *client* from that image, not its server.

Built by an explicit `make bench-image` into `dbtest/bench:dev` — **not** built
inside an activity, which would be slow and surprising. The launch activity fails
with a clear message if the tag is missing. (There is no Makefile in the repo yet;
this adds the first one.)

Because it is an ordinary binary, it also runs bare during development —
`go run ./cmd/bench -dsn …` — skipping Docker entirely. The workflow always uses
the container, since that is the path that must keep working against RDS.

### 5.2 The launcher: `harness/`

Mirrors `provider/` exactly: an interface, a registry, one implementation per
target.

```
provider.Provider        harness.Runner
├─ provider/docker       ├─ harness/docker    ← build now
└─ provider/aws          └─ harness/ecs       ← later
```

```go
package harness

// Spec is what to run — it carries no notion of where.
type Spec struct {
    DSN      string                 // how bench reaches the database
    Workload workload.WorkloadName  // "pgbench"
    Config   workload.Config        // scale, clients, duration
    Init     bool                   // init-only phase, then exit
}

// Handle identifies a started run: container ID locally, task ARN on ECS.
type Handle struct{ ID string }

type Runner interface {
    Start(ctx context.Context, spec Spec) (Handle, error)
    Wait(ctx context.Context, h Handle) error
    Stop(ctx context.Context, h Handle) error
    Result(ctx context.Context, h Handle) (Result, error)
}
```

`Start` is always detached, which yields both shapes without extra methods:

| Phase | Calls |
|---|---|
| init (§3.2) | `Start` → `Wait` → `Stop` |
| measured phase | `Start` → … kill happens … → `Stop` |

The docker implementation is thin — `ContainerCreate`+`ContainerStart`,
`ContainerWait`, `ContainerRemove(Force)`, and `ContainerLogs` scanned for the
`DBTEST_RESULT` sentinel.

**Why a separate package rather than a method on `Provider`.** "Where the database
lives" and "where the load runs" are independent axes:

| Database | Load generator | |
|---|---|---|
| docker, local | local container | what we build now |
| RDS | **local** | **the current setup — the 6 tps bug** |
| RDS | ECS, in-region | the goal |

Three of these are configurations we have actually run or want to. If `Runner`
were a capability of `Provider`, choosing `-provider aws` would force ECS and the
mismatched pairing could no longer be expressed — losing the ability to A/B the
bug against the fix, which is the most valuable comparison Fargate enables.

Secondary: `provider` currently imports only `context`, `fmt`, `sort`, and
`telemetry`. `Spec` carries `workload.Config`, so folding the launcher in would add
a `provider → workload` edge, making the base layer know about benchmarks. The
cost of keeping them apart is three duplicated lines of Docker client
construction.

### 5.3 Provisioning changes (docker provider)

**(a) Pinned Postgres settings** (§3.1). `ContainerCreate` (`docker.go:61`) sets
`Env` and no `Cmd`; the image entrypoint passes `Cmd` through to the server. Don't
hardcode the experiment's values — add a cross-provider field instead:

```go
type ProvisionRequest struct {
    VCPU            float64
    MemoryMiB       int
    DiskGiB         int
    PostgresVersion string
    Settings        map[string]string   // NEW
}
```

Docker renders it as `-c k=v` flags; AWS renders it as an **RDS parameter group**.
Worth encoding now precisely because it looks free locally and is a whole API on
RDS — parameter groups, plus some settings needing a reboot to take effect.

**(b) Container name.** `ContainerCreate` passes `""` today (`docker.go:70`), so
Docker invents a random name that `bench` cannot predict. Derive it from the
provisioning token: `"dbtest-" + token[:8]`. Bonus — the comment at
`docker.go:43-45` says the docker provider ignores `token` because it needs no
idempotency; a fixed name gives it some, since a retried `Provision` hits a name
conflict and can adopt the existing container instead of orphaning one.

**(c) User-defined network.** One long-lived network (`dbtest-net`), created if
absent and never deleted — per-run networks add teardown races for no benefit.

The second address then has to reach the workflow:

```go
type ClusterInfo struct {
    ID       string
    Target   PGTarget   // from the worker  → localhost:54321
    Internal PGTarget   // from a sibling   → dbtest-abc12345:5432   NEW
    Password string
}
```

Docker sets `Internal = {Host: name, Port: 5432}`. AWS sets `Internal = Target` —
the RDS endpoint is identical from anywhere in the VPC, so nothing special-cases
it. Call sites become explicit:

```go
cluster.Target.URL(pw)     // worker: CHECKPOINT, WAL measurement, recovery probe
cluster.Internal.URL(pw)   // bench's -dsn
```

**This is not tidiness.** `PublishAllPorts: true` means Docker assigns a *fresh*
host port on every start — which is why `KillProcess` re-inspects at
`docker.go:204-207`. So across the crash `Target` changes but `Internal` does not.
A load generator pinned to the published port would be permanently broken by the
kill, which is the entire scenario.

### 5.4 Activities

| Activity | Does |
|---|---|
| `InitBench` | harness `Start`+`Wait`+`Stop` with `Init: true` |
| `Checkpoint` | the §3.2 WAL-baseline reset |
| `StartBench` | harness `Start`, detached; returns the `Handle` |
| `StopBench` | harness `Stop` (deferred) |
| `CrashAndMeasure` | the atomic measurement — below |
| `SaveRecoveryResult` | persist one row |

`CrashAndMeasure(cluster, checkpoint bool)` — everything timing-sensitive in one
activity, per §2.2:

1. connect on `Target`; if `checkpoint` → `CHECKPOINT`
2. read `wal_bytes` (§3.4)
3. `t0 = now`; SIGKILL; wait for exit; start container
4. poll with **tight connect + statement timeouts** until `SELECT 1` succeeds → `t1`
5. read container logs since `t0`, parse the redo line
6. return `{wal_bytes, downtime_ms, redo_elapsed_ms}`

Tight timeouts matter: with the OS default a hung TCP connect can block for
minutes, and you would be measuring your own client rather than the database.

### 5.5 Workflow sketch

```go
StartRun
defer EndRun
Provision                      // Settings pinned, named, networked  (§5.3)
defer Deprovision
WaitForReady
InitBench                      // bench -init  →  pgbench -i -s N
Checkpoint                     // §3.2 — WAL baseline now zero
StartBench                     // detached; -duration longer than the load phase
defer StopBench
workflow.Sleep(loadDuration)
CrashAndMeasure(checkpoint)    // §5.4 — the whole measurement, one activity
SaveRecoveryResult
```

### 5.6 bench does not survive the kill — by design

pgbench aborts its clients when the connection drops; it has no reconnect. Here it
is the **WAL generator**, not the measuring instrument — the probe inside
`CrashAndMeasure` does the timing. We do not read bench's result at all in this
workflow, so its TPS number is lost. That's fine: this measures recovery, not
throughput.

(The `ledger` workload later *will* need to survive and report — which is why
`Result` exists on the harness interface even though this workflow never calls
it.)

## 6. Storage

New table in `state/state.go` `migrate()`, matching the existing style:

```sql
CREATE TABLE IF NOT EXISTS recovery_results (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id          UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    provider        TEXT NOT NULL,
    checkpointed    BOOLEAN NOT NULL,   -- which arm
    wal_bytes       BIGINT NOT NULL,
    downtime_ms     FLOAT8 NOT NULL,
    redo_elapsed_ms FLOAT8,             -- NULL when no redo was needed
    load_duration_s FLOAT8 NOT NULL,
    clients         INT NOT NULL,
    scale_factor    INT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

The comparison is then a query:

```sql
SELECT checkpointed,
       count(*)             AS runs,
       avg(downtime_ms)     AS avg_downtime_ms,
       avg(redo_elapsed_ms) AS avg_redo_ms,
       avg(wal_bytes)       AS avg_wal_bytes
FROM recovery_results
GROUP BY checkpointed;
```

## 7. Sanity checks — what would mean the experiment is broken

Worth writing down *before* seeing results, so a broken setup isn't read as a
finding:

- **Arm B reports ~0 `wal_bytes`** → the load never reached the DB. Hard-fail.
- **Both arms report `redo is not required`** → checkpointing wasn't actually
  pinned (§3.1), or the load phase was too short to matter.
- **`downtime_ms` differs but `redo_elapsed_ms` doesn't** → you're measuring
  container restart noise, not recovery. Increase load duration / scale factor.
- **A single run per arm** → not a comparison. Recovery time on a laptop is noisy
  (page cache, disk contention, CPU). Each arm needs repeating; see §8.

## 8. Open decisions

### 8.1 How are the two arms structured?

| Option | Trade-off |
|---|---|
| **One workflow, `Checkpoint bool` flag; run it twice** *(leaning)* | Simplest. Repeating each arm N times is just running it again. Comparison is the SQL in §6. |
| One execution runs both arms, provisioning a fresh cluster for each | Paired comparison in one command; ~2× runtime and more teardown paths to get right. |
| One execution, both arms on the same cluster, sequentially | Fastest, but arm 2 starts from a DB already warmed and reshaped by arm 1 — the arms stop being independent. |

### 8.2 How aggressively should Postgres be tuned?

§3.1 is mandatory either way. The question is whether to **additionally** stack
the deck:

| Option | Trade-off |
|---|---|
| **Pin checkpoints only** *(leaning)* | Everything else at defaults. Smaller effect, but the numbers stay comparable to what RDS will show later. |
| Also `shared_buffers=1GB`, `bgwriter_lru_maxpages=0` | Dirty pages pile up in memory instead of being written out early → maximum gap between arms, but the numbers no longer describe a realistic deployment. |

### 8.3 Load phase size

The risk here is scale factor, not duration.

| Option | Trade-off |
|---|---|
| **60 s, 8 clients, scale 10** *(leaning)* | ~150 MB dataset — larger than default `shared_buffers`, so replay does real I/O. Repeating 5× still finishes in minutes. |
| 30 s, 4 clients, scale 1 | Matches current starter defaults, but ~15 MB fits entirely in shared_buffers. Both arms may recover near-instantly and the comparison comes out flat — from a setup that *could not* have shown a difference. |
| 300 s, 8 clients, scale 50 | Clearest signal, closest to a real production crash. ~6 min per run, so a repeated experiment is an hour-plus. |

### 8.4 Repetitions

Each arm needs several runs to say anything (§7). Do we want `-repeat N` in
`cmd/starter` (workflow IDs need a suffix to avoid collision — the current
default ID is just the workflow name), or is running the command N times by hand
good enough for now?

## 9. Build order

Each step is verifiable before the next exists:

1. **`cmd/bench` + Dockerfile + Makefile** — run it by hand against a manually
   started postgres container. Done when it prints a `DBTEST_RESULT` line.
2. **`harness/` + `harness/docker`** — the workflow can start / stop / collect.
3. **Provider changes** — `Settings`, container name, `dbtest-net`, `Internal`
   (§5.3).
4. **`CrashAndMeasure`** — kill + probe + parse, in one activity (§5.4). The
   delicate one.
5. **`recovery_results` table + save activity** (§6).
6. **`RecoveryWorkflow`** (§5.5).
7. **Run both arms, compare** (§6 query).

ECS is then purely additive — `harness/ecs`, Terraform, and `FailureInjector` for
aws. Steps 1–7 are not touched.

## 10. Later: pointing this at RDS

Nothing above is Docker-specific except the two implementations:

- `aws` does **not** implement `provider.FailureInjector` yet — only `docker`
  does. That's the gap to close.
- When it is implemented, decide what's being measured: `RebootDBInstance` with
  `ForceFailover` is a *failover* test (different failure mode, different
  expected downtime) than without it.
- `harness/ecs` replaces the Docker API with `ecs:RunTask` and the Docker logs
  API with CloudWatch. `cmd/bench` and its image are unchanged.
- Server logs come from the CloudWatch/RDS log API rather than the Docker logs
  API, so `redo_elapsed_ms` parsing needs a per-provider source.
- **The probe has to move in-region.** `CrashAndMeasure` currently polls from the
  worker, which is fine when both are on one laptop (~0.1 ms) but breaks against
  RDS exactly the way pgbench did — ~60 ms of WAN round-trip swamping a
  sub-second recovery. The natural home is `bench`, which is already in-region.
  Worth knowing now: it slightly favors designing `bench` as something that keeps
  running and reporting rather than fire-and-forget — the same conclusion the
  durability discussion reached from the other direction.
