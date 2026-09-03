# Plan: Checkpoint vs. Recovery Time

Measure how long Postgres takes to come back after an ungraceful kill, and how
that time depends on **whether a checkpoint had just happened**. Docker-local
first; the same workflow should later point at RDS unchanged.

Status: §5.1–§5.4 built (`cmd/bench`, `cmd/probe`, `harness/`, provider changes).
The workflow, the activities, and the results table are not.

`CrashRecoveryWorkflow` is left untouched — this is a new, separate workflow.

> **Revision.** The first version of this plan measured downtime from inside a
> single activity (`CrashAndMeasure`) that killed the database and then polled it
> back to life. On review that became a **second container** instead — see §2.2.
> The run now involves three containers: the database, a load generator, and a
> prober.

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

**2.1 — There was no pgbench container.** pgbench ran as a local binary via
`exec.Command`, which also meant the worker host had to have `pgbench` on its
`PATH`.

**Decided:** the load generator became its own binary (`cmd/bench`) in its own
image, used **locally as well as** in the cloud — see §5.1. Running stock
`postgres:16` with an overridden entrypoint would work for this experiment (that
image already ships `/usr/bin/pgbench` — verified, `16.14`), but it is a dead end:
pgbench cannot reconnect, cannot report structured results, and cannot carry the
acked-commit bookkeeping the durability work needs. One artifact, exercised the
same way in both environments, is worth the build step.

**2.2 — Downtime cannot be measured from the worker.** The original
`KillProcess` → `WaitForReady` split pays a full Temporal round-trip between the
SIGKILL and the first probe — activity completion → workflow task → activity
dispatch, plausibly 50–500 ms locally, the same order of magnitude as the effect
being measured. The result would be mostly Temporal.

The first fix was to fold both into one `CrashAndMeasure` activity. **That was
rejected in review in favour of a second container that runs the polling loop.**
Three reasons, in increasing order of importance:

1. **It provisions in the cloud.** The measurement has to end up in-region. An
   activity polls from wherever the worker happens to run, which is fine when
   worker and database share a laptop (~0.1 ms) and useless against RDS, where
   ~60 ms of WAN round-trip swamps a sub-second recovery — the same way it
   already ruined the local pgbench numbers. A container is placed where the
   database is; an activity is placed where the worker is. Only one of those is
   a knob we control.
2. **It can carry the consistency check too.** The prober is the only process
   that spans the crash with a connection it keeps re-establishing, which makes
   it the natural home for "what was acked before the kill is still there after
   it" — the acked-commit watermark from the PR #16 review. Downtime and
   durability are two readings off one instrument, not two subsystems.
3. **A hot loop does not belong in a Temporal activity.** An activity spinning
   at 20 ms for the length of an outage holds a worker slot for its duration,
   has to heartbeat to stay cancellable, and on retry restarts the loop from
   scratch — losing the pre-crash baseline that gives the measurement its
   meaning. Temporal wants activities short, idempotent, and retryable; a
   stopwatch is none of those.

> **The constraint inverts.** The original rule was *kill and probe must happen
> inside one activity*. The rule now is **the measurement never crosses a process
> boundary at all**: the prober records `last_ok` and `first_ok_after` against
> its own monotonic clock, and the workflow only reads the number afterwards.
> Nothing timing-sensitive is left in the activity layer, so the kill goes back
> to being the ordinary `KillProcess` activity that already exists.

## 3. Experimental controls

### 3.1 Pin checkpointing (mandatory — not yet implemented)

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

> **Gap.** `ProvisionRequest` has no `Settings` field and the docker provider
> passes no `Cmd`, so nothing is pinned today. This is the one control whose
> absence invalidates the whole experiment rather than degrading it — build it
> first (§5.4a).

### 3.2 Reset the WAL baseline after `pgbench -i` (mandatory)

`pgbench -i -s N` writes a lot of WAL. With checkpointing pinned off (§3.1) that
WAL is *never* flushed away on its own. So both arms must issue a `CHECKPOINT`
**after init, before the load phase starts.**

Without this, arm B's WAL backlog is `init + load` instead of `load`, and the two
arms differ by more than the one variable we're testing.

### 3.3 Identical load in both arms

Same duration, same clients, same scale factor — and **the same prober, at the
same interval**, running for the same span. The prober opens a fresh connection
per sample (§5.2), which costs the server a backend fork each time; that is real
load, so it has to be a constant rather than something only one arm pays. The
arms differ by exactly one statement: whether `CHECKPOINT` runs immediately
before the kill.

### 3.4 Record the independent variable

Before the SIGKILL:

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

Note that this is no longer read in the same activity as the kill, so `bench`
must be stopped **before** the reading — otherwise WAL keeps growing between the
measurement and the kill and the recorded value undercounts by an arbitrary
round-trip's worth. See §8.5.

## 4. Metrics

All recorded per run.

| Metric | Source | What it means |
|---|---|---|
| `downtime_ms` | prober: `longest_down_ms` | Last successful query before the kill → first successful query after it. End-to-end, includes container restart. What a user feels. |
| `redo_elapsed_ms` | parsed from the server log | WAL replay in isolation, free of restart noise. **The number that should differ between arms.** |
| `wal_bytes` | §3.4 | The input that explains the output. |
| `errors` | prober: per-sample classification | How the outage decomposed — `refused` (nothing listening) vs. `57P03` (up, still replaying). Free, and it tells you *which phase* got longer. |

**`downtime_ms` is `last_ok → first_ok_after`, not `sigkill → first_ok`.** The
prober cannot see the SIGKILL, so it brackets the outage with its own
observations, which is up to one poll interval wider than the true downtime. The
alternative — splicing the kill activity's timestamp into the prober's timeline —
is rejected: locally it buys one interval of accuracy, and against RDS it silently
compares two machines' clocks, where the skew is indistinguishable from the
signal. A self-contained measurement with a known bias beats a cross-process one
with an unknown one.

That makes the poll interval part of the result, so it is stored alongside it
(§6). At `-interval 100ms` (the prober's default) a ~1 s recovery carries ±10%,
which is worse than the Temporal round-trip §2.2 rejected; run the experiment at
**20 ms** and keep it identical across arms.

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

> **Open: where does the server log come from?** `harness.Runner.Logs` reads
> containers *the harness started* — bench and probe. The database container
> belongs to `provider`, which exposes no log method, so `redo_elapsed_ms` has no
> code path today. See §8.6.

> **Open unknown, to be answered by data rather than argued:** Postgres performs
> an end-of-recovery checkpoint, and whether it completes before or after the
> postmaster starts accepting connections is version- and path-dependent. Rather
> than reason about it, we measure both `downtime_ms` and `redo_elapsed_ms` and
> see how the gap behaves across arms.

## 5. Architecture

Four layers, each ignorant of the ones below it:

```
workflow          orders the phases, owns the arms
      │
worker/activity   starts containers, reads their stdout, persists results
      │
harness           launches a container somewhere        ← docker now, ecs later
      │
cmd/bench         generates load           cmd/probe    measures availability
```

Neither binary learns where it is running; `harness` never learns what it
started. That is what makes Docker→ECS swappable without touching either binary.

| Concern | Name |
|---|---|
| Binary that runs workloads | `cmd/bench` → image `dbtest/bench:dev` |
| Binary that measurecan I create s availability | `cmd/probe` → image `dbtest/probe:dev` |
| Package that launches them | `harness/` (+ `harness/docker`, later `harness/ecs`) |
| Workflow | `RecoveryWorkflow` |
| Results table | `recovery_results` |

A run therefore has **three containers**: the database (owned by `provider`), the
load generator, and the prober (both owned by `harness`).

### 5.1 The load generator: `cmd/bench`

A standalone binary in a standalone image. Locally it runs as a Docker container
next to the database container; on Fargate the identical image runs as an ECS
task. The only thing that differs is who launches it and where its logs are read.

**Why a binary and not just `pgbench`.** pgbench is a one-shot CLI: it aborts its
clients when the connection drops, writes only human-readable text to stdout, and
holds no state you can query. The wrapper is what buys a structured result,
signal handling, and — later — the acked-commit watermark.

**It is a thin runner over the existing `workload` registry** — not a new
abstraction. `workload.New(name, cfg)` already exists with `pgbench` and
`warehouse` registered via `init()`. So `main()` is: parse flags → resolve the
workload → `Run(ctx, dsn, tel)` → print the result.

```
bench -workload pgbench -dsn … -scale 10 -clients 8 -duration 60s
bench -init -workload pgbench -dsn …          # setup phase only, then exit
```

`-init` runs only `workload.Initializer`, which exists as an optional capability
mirroring `provider.FailureInjector`. Splitting it out is what lets the workflow
put the reset `CHECKPOINT` between init and load (§3.2). That `CHECKPOINT` stays
in the **workflow** rather than inside `bench`: it is experiment-specific, it is
not timing-sensitive, and keeping it out leaves `bench` generic.

| Workload | Status |
|---|---|
| `pgbench` | built — shells out to `pgbench`, reuses the existing output parsing |
| `ledger` | later — native `pgx` writers with the per-writer acked-sequence watermark |

**Result protocol.** One JSON object per line on stdout, nothing else; every log
line goes to stderr where it cannot corrupt it. Read locally through the Docker
logs API, on Fargate through CloudWatch. Chosen over an HTTP endpoint because it
needs **no inbound network path to the container**: a Fargate task in a private
subnet cannot be polled from a laptop without a load balancer.

> **Metrics move up a layer.** Prometheus gauges are *scraped*, which only works
> for a long-lived process — a container that runs 60 s and exits will never be
> polled, and on Fargate it has no reachable address at all. So `bench` **pushes**
> its result out on stdout, the harness carries it back, and the **worker** sets
> the gauges.

**Exit semantics.** Losing the connection mid-run is the expected outcome in a
crash test. `bench` emits what it has with `"aborted": true` and then exits
non-zero, because a workload that did not finish did not succeed. **The workflow
must therefore ignore bench's exit code** — this run kills the database on
purpose (§5.7).

### 5.2 The measuring instrument: `cmd/probe`

The container from §2.2. It opens a connection, runs `SELECT 1`, closes it, and
repeats — recording when that stops working and when it starts again.

```
probe -dsn … -interval 20ms -timeout 2s -recovered-after 20 -max-duration 10m
```

Four things make it a measurement rather than a health check:

- **A fresh connection per sample.** A held connection can keep answering long
  after the server would refuse a new one; "can a client connect *and* query" is
  the thing being measured, so both halves are re-done every time.
- **Tight per-sample timeouts.** With the OS default a hung TCP connect blocks
  for minutes, and you would be timing your own client rather than the database.
- **A run of successes ends the outage, not the first one.** A database
  mid-recovery will accept one connection and refuse the next; `-recovered-after`
  requires N consecutive successes before believing it. This affects *when the
  prober exits*, not the recorded downtime — `first_ok_after` is stamped at the
  first success and only revised if the recovery turns out not to have stuck.
- **It self-terminates.** Once it has watched an outage begin and end it exits,
  so the workflow waits for it rather than guessing how long to sleep.
  `-max-duration` bounds the case where the outage never comes.

**Classification, not just counting.** Each failure is bucketed by SQLSTATE where
there is one (`57P03` = up but still replaying) and by syscall otherwise
(`refused`, `reset`, `timeout`). That histogram is what distinguishes "the
container took longer to restart" from "replay took longer," independently of the
server log.

**Startup ordering.** Outage detection is gated on having seen a first success —
otherwise "the database was never up" would be indistinguishable from "the
database went down." So the prober is started **right after `WaitForReady`, before
the init phase**, giving it the whole run to establish a baseline. This is also
why §3.3 requires it in both arms: it is background load, and background load has
to be a constant.

If the kill somehow lands before that first success, the prober reports
`recovered: false` with `"no completed outage observed"` and the run **hard-fails**
rather than reporting a plausible number.

**Where it grows next.** Reason 2 in §2.2: the prober is the process that spans
the crash, so the durability check belongs here — read back the acked watermark
after recovery and compare. Downtime and consistency come off one instrument.

### 5.3 The launcher: `harness/`

Mirrors `provider/` exactly: an interface, a registry, one implementation per
target.

```
provider.Provider        harness.Runner
├─ provider/docker       ├─ harness/docker    ← built
└─ provider/aws          └─ harness/ecs       ← later
```

```go
package harness

// Spec is what to run — it carries no notion of where.
type Spec struct {
    Image   string
    Args    []string // appended to the image's entrypoint
    Name    string
    Network string
}

// Handle identifies a started container: a container ID on Docker, a task ARN
// on ECS.
type Handle struct{ ID string }

type Runner interface {
    Start(ctx context.Context, spec Spec) (Handle, error)
    Wait(ctx context.Context, h Handle) (int, error)   // exit code
    Stop(ctx context.Context, h Handle) error          // stop AND remove
    Output(ctx context.Context, h Handle) ([]byte, error) // stdout — the result
    Logs(ctx context.Context, h Handle) ([]byte, error)   // stderr — diagnostics
}
```

**`Spec` is image-and-args, not workload-and-config.** That is what lets the same
harness launch `probe` as well as `bench` — the launcher has no opinion about
what it is launching, so a third binary costs nothing. It also keeps `harness`
from importing `workload`.

**`Output` and `Logs` are separate because stdout and stderr are.** Both binaries
promise stdout carries only the result; splitting the streams at the harness
boundary is what makes that promise usable. (Requires `Tty: false` on the
container, so the streams stay framed.)

`Start` is always detached, which yields every shape without extra methods:

| Phase | Calls |
|---|---|
| init (§3.2) | `Start` → `Wait` → `Output` → `Stop` |
| load | `Start` → … kill happens … → `Stop` |
| probe | `Start` → … kill happens … → `Wait` → `Output` → `Stop` |

**`Stop` removes the container, so `Output` must come first.** The interface says
so; nothing enforces it, and a deferred `StopProbe` makes it easy to invert. §5.5
handles this by keeping read-then-stop inside one activity.

**Why a separate package rather than a method on `Provider`.** "Where the database
lives" and "where the load runs" are independent axes:

| Database | Load generator | |
|---|---|---|
| docker, local | local container | what we build now |
| RDS | **local container** | the current setup — the 6 tps bug |
| RDS | ECS, in-region | the goal |

Three of these are configurations we have actually run or want to. If `Runner`
were a capability of `Provider`, choosing `-provider aws` would force ECS and the
mismatched pairing could no longer be expressed — losing the ability to A/B the
bug against the fix, which is the most valuable comparison Fargate enables. The
cost of keeping them apart is three duplicated lines of Docker client
construction.

### 5.4 Provisioning changes (docker provider)

**(a) Pinned Postgres settings** (§3.1) — **not yet built.** `ContainerCreate`
sets `Env` and no `Cmd`; the image entrypoint passes `Cmd` through to the server.
Don't hardcode the experiment's values — add a cross-provider field instead:

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

**(b) Container name** — built. Derived from the provisioning token
(`"dbtest-" + token`) so siblings can address it, and so a retried `Provision`
hits a name conflict and adopts the existing container instead of orphaning one.

**(c) User-defined network** — built. One long-lived network (`dbtest-net`),
created if absent and never deleted; per-run networks add teardown races for no
benefit. Both harness containers join it.

The second address then has to reach the workflow:

```go
type ClusterInfo struct {
    ID       string
    Target   PGTarget   // from the worker  → localhost:54321
    Internal PGTarget   // from a sibling   → dbtest-<token>:5432
    Password string
}
```

Docker sets `Internal = {Host: name, Port: 5432}`. AWS sets `Internal = Target` —
the RDS endpoint is identical from anywhere in the VPC, so nothing special-cases
it. Call sites become explicit:

```go
cluster.Target.URL(pw)     // worker: CHECKPOINT, WAL measurement
cluster.Internal.URL(pw)   // bench's and probe's -dsn
```

**This is not tidiness.** `PublishAllPorts: true` means Docker assigns a *fresh*
host port on every start — which is why `KillProcess` re-inspects afterwards. So
across the crash `Target` changes but `Internal` does not. **A prober pinned to
the published port would be permanently broken by the kill and would report an
infinite outage** — it must use `Internal`.

### 5.5 Activities

| Activity | Does |
|---|---|
| `StartBench` / `StartProbe` | harness `Start`, detached; returns the `Handle` |
| `RunBenchInit` | `Start` + `Wait` + `Stop` with `-init` |
| `Checkpoint` | the §3.2 WAL-baseline reset, and the arm-A pre-kill checkpoint |
| `MeasureWAL` | §3.4, on `cluster.Target` |
| `KillProcess` | **already exists** — `provider.FailureInjector` |
| `CollectProbe` | `Wait` (bounded) → `Output` → parse → `Stop`, in that order |
| `StopBench` | `Stop`, deferred, best-effort |
| `ReadRedo` | server log since the kill → `redo_elapsed_ms` (§8.6) |
| `SaveRecoveryResult` | persist one row |

Nothing here is timing-sensitive: the clock lives in the prober. `KillProcess`
needs no changes at all, which is the clearest evidence the container split was
the right call.

`CollectProbe` is one activity rather than three because `Stop` removes the
container that `Output` reads from (§5.3). The deferred `Stop` remains as a
best-effort second call — it already treats "not found" as success.

### 5.6 Workflow sketch

```go
StartRun
defer EndRun
Provision                      // Settings pinned, named, on dbtest-net  (§5.4)
defer Deprovision              // registered first → runs last
WaitForReady

StartProbe                     // -dsn = cluster.Internal; baseline before anything else
defer StopProbe

RunBenchInit                   // bench -init  →  pgbench -i -s N
Checkpoint                     // §3.2 — WAL baseline now zero
StartBench                     // detached, -dsn = cluster.Internal
defer StopBench
workflow.Sleep(loadDuration)

StopBench                      // WAL stops growing before we read it     (§8.5)
MeasureWAL                     // §3.4 — the independent variable
if arm == A { Checkpoint }     // the one statement the arms differ by
KillProcess                    // ordinary activity; returns refreshed Target

CollectProbe                   // blocks until the prober has seen recovery
ReadRedo
SaveRecoveryResult
```

The deferred teardown unwinds `StopBench` → `StopProbe` → `Deprovision`, so both
harness containers are gone before the database they point at is.

### 5.7 bench does not survive the kill — by design

pgbench aborts its clients when the connection drops; it has no reconnect. Here it
is the **WAL generator**, not the measuring instrument — the prober does the
timing. We do not read bench's result in this workflow, so its TPS number is lost.
That's fine: this measures recovery, not throughput. Its non-zero exit is expected
and must not fail the workflow.

(The `ledger` workload later *will* need to survive and report — which is why
`Output` exists on the harness interface even though this workflow ignores
bench's.)

## 6. Storage

New table in `state/state.go` `migrate()`, matching the existing style:

```sql
CREATE TABLE IF NOT EXISTS recovery_results (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id            UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    provider          TEXT NOT NULL,
    checkpointed      BOOLEAN NOT NULL,   -- which arm
    wal_bytes         BIGINT NOT NULL,
    downtime_ms       FLOAT8 NOT NULL,    -- prober: longest_down_ms
    redo_elapsed_ms   FLOAT8,             -- NULL when no redo was needed
    probe_interval_ms FLOAT8 NOT NULL,    -- the measurement's resolution (§4)
    probe_failures    INT NOT NULL,
    probe_errors      JSONB NOT NULL,     -- classification → count
    load_duration_s   FLOAT8 NOT NULL,
    clients           INT NOT NULL,
    scale_factor      INT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

`probe_interval_ms` is not bookkeeping: it bounds the error on `downtime_ms`
(§4), so rows recorded at different intervals are not comparable and the query
has to be able to tell.

The comparison is then a query:

```sql
SELECT checkpointed,
       count(*)             AS runs,
       avg(downtime_ms)     AS avg_downtime_ms,
       avg(redo_elapsed_ms) AS avg_redo_ms,
       avg(wal_bytes)       AS avg_wal_bytes
FROM recovery_results
WHERE probe_interval_ms = 20
GROUP BY checkpointed;
```

## 7. Sanity checks — what would mean the experiment is broken

Worth writing down *before* seeing results, so a broken setup isn't read as a
finding:

- **Arm B reports ~0 `wal_bytes`** → the load never reached the DB. Hard-fail.
- **The prober reports `recovered: false`** → it never bracketed an outage: it
  started too late, or the database never came back. Hard-fail; the run has no
  `downtime_ms` at all, which is different from having a large one.
- **Both arms report `redo is not required`** → checkpointing wasn't actually
  pinned (§3.1), or the load phase was too short to matter.
- **`downtime_ms` differs but `redo_elapsed_ms` doesn't** → you're measuring
  container restart noise, not recovery. Increase load duration / scale factor.
  The prober's error histogram should corroborate: restart noise shows up as
  `refused`, replay as `57P03`.
- **`downtime_ms` is within a few multiples of `probe_interval_ms`** → the
  quantization is the same size as the answer. Lower the interval before
  believing the difference.
- **A single run per arm** → not a comparison. Recovery time on a laptop is noisy
  (page cache, disk contention, CPU). Each arm needs repeating; see §8.4.

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

### 8.5 Is `bench` stopped before the measurement?

§3.4's reading is no longer in the same activity as the kill, so WAL grows
between them unless the load is stopped first.

| Option | Trade-off |
|---|---|
| **Stop bench, then measure, then kill** *(leaning)* | `wal_bytes` is exact and both arms are cleanly comparable. The kill lands on an idle database — no in-flight transactions to abort, which changes replay volume negligibly but does make the crash less realistic. |
| Measure while bench runs, then kill | Keeps the crash realistic (a real one interrupts traffic), but `wal_bytes` undercounts by however much WAL landed during one activity round-trip — an error that varies per run and is invisible in the data. |

The second is more faithful to production; the first is more defensible as a
measurement. Given the goal is a *comparison* rather than an absolute number,
leaning toward the first.

### 8.6 Where does `redo_elapsed_ms` come from?

`harness.Runner.Logs` only reaches harness-started containers; the database
belongs to `provider`, which has no log method (§4).

| Option | Trade-off |
|---|---|
| **`provider.LogReader` optional capability** *(leaning)* | Mirrors `FailureInjector`. Docker reads container logs `Since: t0`, AWS reads the RDS log API — so §10's "per-provider log source" is solved by construction rather than deferred. One interface. |
| Drop `redo_elapsed_ms`, ship on `downtime_ms` + `wal_bytes` | Cheaper now, but §7's "restart noise vs. replay" check stops being decidable from the server's own account — only the prober's error histogram remains, which is suggestive rather than conclusive. |

## 9. Build order

Each step is verifiable before the next exists.

| # | Step | Status |
|---|---|---|
| 1 | `cmd/bench` + image + Makefile | **done** — prints its result line |
| 2 | `cmd/probe` + image | **done** — brackets an outage against a hand-killed container |
| 3 | `harness/` + `harness/docker` | **done** — start / wait / output / stop |
| 4 | Provider: container name, `dbtest-net`, `Internal` (§5.4b–c) | **done** |
| 5 | Provider: `ProvisionRequest.Settings` (§5.4a) | **blocks a valid result** |
| 6 | `recovery_results` table + `SaveRecoveryResult` (§6) | |
| 7 | `RecoveryWorkflow` + its activities (§5.5, §5.6) | |
| 8 | `ReadRedo` / `provider.LogReader` (§8.6) | |
| 9 | Run both arms, compare (§6 query) | |

Step 5 first: without it every later step produces numbers that look fine and
mean nothing.

ECS is then purely additive — `harness/ecs`, Terraform, and `FailureInjector` for
aws. Steps 1–9 are not touched.

## 10. Later: pointing this at RDS

Nothing above is Docker-specific except the implementations:

- `aws` does **not** implement `provider.FailureInjector` — only `docker` does.
  Until it does, `RecoveryWorkflow` fails at the kill step against RDS. That's
  the gap to close, and it is the only thing making this Docker-only.
- When it is implemented, decide what's being measured: `RebootDBInstance` with
  `ForceFailover` is a *failover* test (different failure mode, different
  expected downtime) than without it.
- `harness/ecs` replaces the Docker API with `ecs:RunTask` and the Docker logs
  API with CloudWatch. `cmd/bench`, `cmd/probe`, and their images are unchanged.
- Server logs come from the CloudWatch/RDS log API rather than the Docker logs
  API — which is what §8.6's `LogReader` is for.
- **The probe is already in-region**, which was reason 1 in §2.2. The measurement
  that would otherwise have to be redesigned for the cloud is the one part that
  needs no change at all. The intermediate config — RDS with the prober still
  launched locally — is worth running deliberately once: it reproduces the WAN
  distortion on purpose, and the difference between the two is the size of the
  error the container split removed.
- Secrets reach the containers as command-line `Args` today, which are visible in
  `docker inspect` and would be visible in an ECS task definition. `Spec` needs an
  `Env` field before a generated RDS password goes through it.
