# Plan: Downtime and Recovery Across Managed Postgres Providers

When a managed Postgres instance is disrupted, **how long is it unavailable, and
is committed data intact when it comes back?** Measured the same way across RDS,
Aurora, CloudSQL, Azure, and Neon so the numbers are comparable.

Docker is the **development target only** — the path that lets the workflow be
built and debugged for free before it spends money. It is not a provider under
comparison.

Status: `cmd/bench`, `cmd/probe`, `harness/`, `harness/docker` and the docker
provider changes are built. The workflow, `harness/ecs`, the disruption API, and
the results table are not.

> **Revision history.** This started as a plan to measure Postgres crash recovery
> against WAL-since-checkpoint (§10). That experiment cannot run on a managed
> provider — none of them expose an ungraceful kill, and a graceful restart
> flushes the very state the experiment varies. The measurement apparatus it
> produced is the right one; the hypothesis was the wrong one for the target.

---

## 1. Why the disruption is the independent variable

The original hypothesis varied WAL volume and measured redo time. On a managed
provider that isn't available to vary. **A graceful restart ends in a shutdown
checkpoint:** every dirty buffer is written, the WAL is flushed, and the control
file is marked cleanly shut down. On restart the server finds nothing to replay.
There is no dirty state to recover from because the shutdown path erases it.

So in the cloud, "recovery" is not WAL replay. It is some combination of process
restart, host or storage reattachment, standby promotion, and DNS re-resolution —
and which of those dominates depends on **which disruption you asked for.** That
is the variable the workflow controls.

### 1.1 What each provider actually offers

| Provider | Graceful restart | Failover | Ungraceful crash |
|---|---|---|---|
| RDS PostgreSQL | `RebootDBInstance` | `RebootDBInstance` + `ForceFailover` (Multi-AZ only) | none |
| Aurora PostgreSQL | `RebootDBInstance` | `FailoverDBCluster` | fault-injection functions — **verify** |
| CloudSQL | `instances.restart` | `instances.failover` (HA only) | none |
| Azure Flexible Server | restart | forced failover (zone-redundant HA only) | none |
| Neon | compute restart / suspend + resume | — | none |
| **docker (dev)** | container restart | — | **SIGKILL** |

Two things fall out of this table, and both change the design:

**Ungraceful kill is a Docker-only capability.** `provider.FailureInjector` is
currently `KillProcess`, modelled on the one provider that can do it — the
narrowest possible case generalized from. Aurora's fault-injection functions are
the one managed exception worth checking, and if they hold, Aurora is the only
place the §10 experiment could run in the cloud; it would test Aurora's claim that
its storage layer removes startup replay entirely, which is the most interesting
single comparison in the project.

**Failover and restart are different measurements, not settings of one.** A
restart returns the same endpoint pointing at the same instance. A failover
returns the same endpoint pointing at a *different* instance, after a DNS change.
Comparing "RDS downtime" to "Aurora downtime" without saying which one is
meaningless, so the disruption has to be recorded on the row (§6) and named at
the API (§2.1).

## 2. Design consequences

### 2.1 `FailureInjector` becomes a disruption API

`KillProcess` cannot express what the providers do. Replace it:

```go
type Disruption string

const (
    Restart  Disruption = "restart"  // graceful; every provider has one
    Failover Disruption = "failover" // requires an HA topology
    Crash    Disruption = "crash"    // ungraceful; docker, maybe Aurora
)

// Disruptor is an optional provider capability, like FailureInjector was.
type Disruptor interface {
    // Supports reports whether this provider can perform d against this cluster.
    // Topology-dependent: Failover is false for a single-AZ RDS instance.
    Supports(cluster ClusterInfo, d Disruption) bool

    // Disrupt applies d and returns refreshed connection info.
    Disrupt(ctx context.Context, cluster ClusterInfo, d Disruption) (ClusterInfo, error)
}
```

`Supports` is separate from `Disrupt` because **the workflow must check it before
provisioning, not at the point of use.** Discovering "aws cannot crash" after
creating an RDS instance and running a load phase costs ten minutes and real
money; discovering it in the first activity costs nothing. It takes `ClusterInfo`
because the answer is topology-dependent — the same provider supports failover or
not depending on whether the request asked for Multi-AZ.

### 2.2 The probe must measure *writability*, not reachability

This is the gap that matters most, and `cmd/probe` has it today.

The prober runs `SELECT 1`. After a failover that succeeds against an endpoint
that has not finished becoming a primary — and it would succeed against a reader
outright. **A database you can read is not a database that is back.** For a
failover the number people care about is time-to-first-successful-write.

So the probe grows a second signal. Three levels, in increasing strength:

| Level | Test | What it catches |
|---|---|---|
| readable | connect + read the counter | the process is up and serving reads |
| writable | advance the counter, get it back | promotion finished; not in recovery |
| durable | the returned value never goes backwards | commits survived the disruption |

The elegant part is that one mechanism gives all three. The prober keeps a single
counter row, advances it by one per sample, and remembers the highest value the
server handed back. Getting a value back proves writability. Because the counter
is an ordinary row, it shares its transaction's fate: a lost commit takes it
backwards, so the first successful write after an outage returns a value at or
below the previous high, and the shortfall is the number of acknowledged commits
lost. That is the acked-commit watermark from the PR #16 review, living in the
prober — which is exactly the convergence that justified making the prober a
container in the first place (§2.4, reason 2).

A Postgres `SEQUENCE` cannot do this. `nextval` is non-transactional and its
advances are WAL-logged in blocks, so after a crash it resumes *ahead* of the
last value handed out. It jumps forward, never backward, and would report no loss
in the one case being measured.

```sql
CREATE TABLE IF NOT EXISTS dbtest_probe (id INT PRIMARY KEY, seq BIGINT NOT NULL, ts TIMESTAMPTZ NOT NULL DEFAULT now());
INSERT INTO dbtest_probe (id, seq) VALUES (1, 0) ON CONFLICT (id) DO NOTHING;
-- readable: SELECT seq FROM dbtest_probe WHERE id = 1
-- writable: UPDATE dbtest_probe SET seq = seq + 1, ts = now() WHERE id = 1 RETURNING seq
```

Two costs, both acceptable and both requiring the prober to be **identical across
every run being compared**: the writes generate WAL and load, and the table must
exist before probing starts.

> Losing an acked commit is a correctness failure, not a slow recovery. It should
> fail the run loudly, separately from any downtime number.

### 2.3 DNS is part of the measurement

On failover the endpoint CNAME is repointed. A client that resolved once and
cached the address connects to the old instance until its cache expires — so the
observed downtime legitimately includes DNS propagation, and a prober that
accidentally caches would report a longer outage than a well-behaved application
sees.

Opening a fresh connection per sample (which `cmd/probe` already does, for the
independent reason that a held connection keeps working after the server would
refuse a new one) means re-resolving per sample. That makes DNS part of the
measurement honestly rather than by accident, and it is the right choice — it is
what a real client experiences. Worth recording the endpoint's TTL alongside the
result so a failover number can be read as "promotion + up to one TTL."

### 2.4 The prober still belongs in a container — for re-ranked reasons

The three original reasons survive the reframe, but their weights change:

1. **A hot loop does not belong in a Temporal activity.** *Now the strongest.* An
   RDS reboot takes 60–120 s and provisioning takes minutes; an activity polling
   at 20 ms across that holds a worker slot for its duration, must heartbeat to
   stay cancellable, and on retry restarts the loop from scratch — losing the
   pre-disruption watermark that gives §2.2 its meaning.
2. **It carries the consistency check.** *Unchanged, and now concrete* — §2.2 is
   what this reason was pointing at.
3. **In-region placement.** *Weaker for slow disruptions, still decisive for fast
   ones.* ~60 ms of WAN round-trip against a 90-second RDS reboot is 0.07% and
   would be tolerable. Against a Neon cold start or an Aurora failover it is not.
   Since the comparison spans both, the measurement has to be placed where the
   fastest provider is still measurable — otherwise the instrument's error is a
   function of which provider you're testing, which is the one thing a comparison
   cannot afford.

### 2.5 Provision once, disrupt many times

The original plan leaned toward "one workflow, run it twice," which was right when
each arm needed a pristine database — the checkpoint arms reshaped state.

Cloud economics invert this. An RDS instance takes 5–10 minutes to create and
minutes more to delete, so a fresh cluster per repetition means ~15 minutes of
provisioning per data point. And a restart is *repeatable in a way a crash arm
was not*: it leaves the database in the same logical state it found it. So the
workflow provisions once and applies the disruption N times, settling between
repetitions.

The prober spans all N, which is why its result is a **list** of outages rather
than one — `cmd/probe` models it that way, and `-repetitions` says how many
disruptions to watch for before exiting.

## 3. Architecture

Unchanged by the reframe, which is the strongest evidence it was the right shape.
Four layers, each ignorant of the ones below:

```
workflow          orders the phases, owns the repetitions
      │
worker/activity   starts containers, reads their stdout, persists results
      │
harness           launches a container somewhere      ← docker (dev), ecs (real)
      │
cmd/bench         generates load         cmd/probe    measures availability
```

| Concern | Name |
|---|---|
| Binary that runs workloads | `cmd/bench` → `dbtest/bench:dev` |
| Binary that measures availability and durability | `cmd/probe` → `dbtest/probe:dev` |
| Package that launches them | `harness/` (+ `harness/docker`, `harness/ecs`) |
| Workflow | `DowntimeWorkflow` |
| Results table | `downtime_results` |

A run has three containers: the database (owned by `provider`), the load
generator, and the prober (both owned by `harness`).

### 3.1 `harness/ecs` is no longer optional

The old plan filed ECS under "later, purely additive." With the cloud as the
primary target and §2.4's placement requirement, it is on the critical path: the
prober has to run in-region for the numbers to mean anything.

`harness.Spec` is already the right shape for it —

```go
type Spec struct {
    Image   string
    Args    []string
    Name    string
    Network string
}
```

— because it is image-and-args rather than workload-and-config, so the launcher
has no opinion about what it launches. `harness/ecs` replaces `ContainerCreate`
with `ecs:RunTask`, `ContainerWait` with a task-stopped waiter, and the Docker
logs API with CloudWatch Logs. Neither binary changes.

Two things `Spec` still needs before it can reach ECS:

- **`Env map[string]string`.** Secrets go in as `Args` today, which are visible in
  `docker inspect` and would land in an ECS task definition and CloudTrail.
- **Placement** — subnets and security groups. Either on `Spec` as an opaque
  per-runner field or resolved from configuration inside `harness/ecs`. The
  latter keeps `Spec` portable; see §7.3.

**Intermediate step worth taking deliberately:** RDS with the prober still
launched locally in Docker. It works today, it is the cheapest way to get a first
end-to-end cloud number, and running it alongside the in-region version measures
the size of the error that in-region placement removes. That difference is worth
one deliberate run, not a guess.

### 3.2 What `bench` is for here

The load generator is no longer generating the independent variable — nothing
about a graceful restart depends on how much WAL is outstanding. It stays for two
reasons: **downtime under load is a different number than downtime at idle**
(connection storms on recovery, cold buffer cache), and the durability check in
§2.2 is only interesting if there was concurrent traffic to be lost.

It dies at the disruption and does not reconnect — pgbench aborts its clients when
the connection drops. That's expected; its non-zero exit must not fail the run.
The prober is the instrument, `bench` is the weather.

### 3.3 Provisioning: `Settings` and topology

`ProvisionRequest` needs two additions, for different reasons.

```go
type ProvisionRequest struct {
    VCPU             float64
    MemoryMiB        int
    DiskGiB          int
    PostgresVersion  string
    HighAvailability bool               // NEW — Multi-AZ / HA / zone-redundant
    Settings         map[string]string  // NEW
}
```

**`HighAvailability`** is what makes `Failover` supportable (§2.1) and is itself
one of the things being compared — the downtime difference between a single-AZ
restart and a Multi-AZ failover is a headline number for choosing a provider.
Each provider renders it its own way: `MultiAZ` on RDS, a second instance in an
Aurora cluster, `availabilityType: REGIONAL` on CloudSQL, zone-redundant HA on
Azure.

**`Settings`** is *not* justified by this experiment the way the original doc
claimed — no downtime measurement needs `checkpoint_timeout` pinned. It is
justified by the comparison being fair. Vendors ship different defaults (RDS
derives `shared_buffers` from instance memory, others do their own thing), so an
unpinned cross-provider comparison is partly a comparison of default tuning
rather than of the platforms. Keys are Postgres GUC names, the one namespace every
vendor already agrees on: Docker renders them as `-c k=v`, RDS as a parameter
group, CloudSQL as flags, Azure as server parameters.

Worth knowing before starting: on RDS this is not a field but a resource. You
create a parameter group, modify it, pass its name to `CreateDBInstance`, and
handle static parameters needing a reboot — and the group must be deleted at
deprovision or it leaks, and cannot be deleted while an instance references it.

### 3.4 Deprovision is a cost control, not a cleanup step

A leaked Docker container is untidy. A leaked RDS instance bills hourly and
silently. The `clusters` table already carries `status`, `provisioned_at`,
`deprovisioned_at`, and `heartbeat_at` — that is the leak detector, and this
workflow is the first one where it earns its keep. Two requirements:

- `Deprovision` runs from a disconnected context in a deferred block, registered
  immediately after `Provision`, so a workflow failure or cancellation still tears
  down. This is the pattern `CrashRecoveryWorkflow` already uses.
- Both harness containers/tasks stop before the database they point at, so
  teardown unwinds `StopBench` → `StopProbe` → `Deprovision`.

A sweeper for rows still `provisioned` with a stale heartbeat is the backstop for
the case where the worker dies outright. Out of scope here, but this is the
workflow that makes it necessary.

## 4. Metrics

Recorded per **disruption**, not per run — a run produces N rows.

| Metric | Source | What it means |
|---|---|---|
| `readable_downtime_ms` | prober: last OK → first OK after | The process is answering again. |
| `writable_downtime_ms` | prober: last acked write → first acked write after | The database is *back*. On a failover this is the real number; on a restart the two nearly coincide. |
| `errors` | prober: per-sample classification | How the outage decomposed — `refused` (nothing listening), `57P03` (up, starting up / shutting down), `25006` (read-only transaction), `timeout`, DNS failure. Tells you *which phase* got longer. |
| `lost_commits` | prober: acked watermark vs. `max(seq)` after | Durability verdict. Non-zero fails the run. |
| `probe_interval_ms` | configuration | The resolution of every number above. |

**Both downtime figures are bracketed by the prober's own observations** — last
success before the outage to first success after — not from the moment the
disruption API was called. The prober cannot see that moment, and splicing in the
activity's timestamp would compare two machines' clocks, where skew is
indistinguishable from signal. The bias is a known one, bounded by one poll
interval, in a known direction. A self-contained measurement with a known bias
beats a cross-process one with an unknown one.

That makes the interval part of the result, which is why it is stored beside it:
rows recorded at different intervals are not comparable and the query has to be
able to tell. Run at **20 ms** and keep it fixed across every provider being
compared.

## 5. Workflow sketch

```go
StartRun
defer EndRun

CheckSupported(provider, disruption)   // §2.1 — before spending anything
Provision                              // HighAvailability, Settings   (§3.3)
defer Deprovision
WaitForReady

CreateProbeTable                       // §2.2
StartProbe                             // in-region; baseline before anything else
defer StopProbe

RunBenchInit                           // bench -init
StartBench                             // detached, background load     (§3.2)
defer StopBench

for i := 0; i < repetitions; i++ {
    workflow.Sleep(settleDuration)
    Disrupt(disruption)                // returns refreshed ClusterInfo
    WaitForReady                       // provider-level; the prober measures
}

CollectProbe                           // Wait → Output → parse → Stop
SaveDowntimeResults                    // one row per outage observed
```

`CheckSupported` first is the whole point of §2.1: the run that cannot work should
cost one activity, not one RDS instance.

`CollectProbe` is a single activity because `harness.Stop` removes the container
that `Output` reads from. The interface documents the ordering; nothing enforces
it, and a deferred `StopProbe` makes it easy to invert. The deferred `Stop`
remains as a best-effort second call — it already treats "not found" as success.

## 6. Storage

```sql
CREATE TABLE IF NOT EXISTS downtime_results (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id                 UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    provider               TEXT NOT NULL,
    disruption             TEXT NOT NULL,     -- restart | failover | crash
    high_availability      BOOLEAN NOT NULL,
    repetition             INT NOT NULL,      -- which of the N
    readable_downtime_ms  FLOAT8 NOT NULL,
    writable_downtime_ms   FLOAT8,            -- NULL if writability never returned
    lost_commits           BIGINT NOT NULL,
    probe_interval_ms      FLOAT8 NOT NULL,
    probe_errors           JSONB NOT NULL,
    instance_class         TEXT NOT NULL,     -- comparability across providers
    engine_version         TEXT NOT NULL,
    under_load             BOOLEAN NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

`instance_class`, `engine_version`, and `probe_interval_ms` are not bookkeeping —
they are the conditions under which the number is valid. A cross-provider average
that silently mixes instance sizes or poll resolutions is worse than no number,
because it looks like an answer.

```sql
SELECT provider, disruption, high_availability,
       count(*)                   AS n,
       avg(readable_downtime_ms) AS avg_readable_ms,
       avg(writable_downtime_ms)  AS avg_writable_ms,
       max(writable_downtime_ms)  AS worst_ms,
       sum(lost_commits)          AS lost
FROM downtime_results
WHERE probe_interval_ms = 20
GROUP BY provider, disruption, high_availability
ORDER BY avg_writable_ms;
```

`max` matters as much as `avg` here. Availability is judged on the tail.

## 7. Open decisions

### 7.1 Repetitions per provisioned cluster

§2.5 argues for provisioning once and disrupting N times. The question is N and
the settle time between. Too short and repetition *i+1* starts before the platform
finished reacting to *i* — RDS in particular refuses operations while an instance
is `modifying`. Leaning **N=5, settle 120 s**, with the workflow polling instance
status rather than trusting the sleep.

### 7.2 Is `bench` running during the measurement?

Downtime under load and downtime at idle are different numbers and both are
defensible. Leaning toward **recording `under_load` and measuring both**, since it
is one extra run per provider and the difference is itself a finding.

### 7.3 Where does ECS placement configuration live?

`Spec` is deliberately portable (§3.1), so subnets and security groups don't
belong on it. Either an opaque `map[string]string` on `Spec` that only `ecs` reads
— portable, but untyped and lying about being portable — or configuration held
inside `harness/ecs`, which keeps `Spec` honest at the cost of the runner needing
its own config plumbing. Leaning toward the second.

### 7.4 Does Aurora's fault injection actually work?

If Aurora PostgreSQL can be crashed ungracefully, it is the only managed target
where §10 can run, and it directly tests Aurora's "no startup replay" claim.
Needs verifying against the docs and a live cluster before it goes in the plan.

### 7.5 Terraform or API calls for provisioning?

`provider/aws` creates instances through the SDK today. HA topology and parameter
groups both add resources with lifecycles of their own. Existing docs
(`terraform-deployment.md`) point one way; the current code points the other.
Deciding this before §3.3 gets built avoids doing it twice.

## 8. Sanity checks — what would mean the numbers are wrong

- **The prober reports no completed outage** → it started too late, or the
  disruption never happened. Hard-fail; a missing measurement is not a fast one.
- **`readable_downtime_ms` is within a few multiples of `probe_interval_ms`** →
  quantization is the size of the answer. Lower the interval before believing it.
- **`writable_downtime_ms` equals `readable_downtime_ms` on a failover** → the
  probe is not actually testing writability (§2.2), or the failover didn't happen.
- **`lost_commits > 0`** → not a downtime finding at all. A durability bug, or a
  broken watermark. Investigate before reporting any timing from that run.
- **Downtime is identical across providers** → suspect the measurement, not the
  platforms. Most likely the prober is timing its own client — connect timeout too
  low, or DNS failing uniformly.
- **One repetition per provider** → not a comparison. Cloud disruption times vary
  with control-plane load, which is invisible and not controlled for.

## 9. Build order

| # | Step | Status |
|---|---|---|
| 1 | `cmd/bench` + image | **done** |
| 2 | `cmd/probe` + image | **done** — reachability only |
| 3 | `harness/` + `harness/docker` | **done** |
| 4 | Provider: container name, `dbtest-net`, `Internal` | **done** |
| 5 | `provider.Disruptor` + docker implementation (§2.1) | |
| 6 | Probe: writability, acked watermark, repetitions (§2.2, §2.5) | |
| 7 | `downtime_results` + save activity (§6) | |
| 8 | `DowntimeWorkflow`, validated end-to-end on docker (§5) | |
| 9 | `provider/aws`: `Disruptor`, `HighAvailability`, `Settings` (§3.3) | |
| 10 | First cloud run — RDS, prober still local (§3.1) | |
| 11 | `harness/ecs`; re-run and compare against step 10 | |
| 12 | Remaining providers | |

Steps 5–8 are development on Docker, and that is Docker's entire role: prove the
instrument works where iteration is free, so that step 10 spends money on a
workflow that has already been debugged. Step 11's comparison against step 10 is
what validates §2.4's placement argument with data instead of an assertion.

## 10. Appendix: checkpoint vs. recovery time — the Docker-only experiment

The original hypothesis, kept because it is the sharpest available test that the
prober measures what it claims to.

Postgres crash recovery replays WAL from the redo point of the last completed
checkpoint. Kill the server right after a `CHECKPOINT` and there is nothing to
replay; kill it with WAL outstanding and recovery is proportional to the volume.
So a prober that is genuinely measuring recovery will show a difference between
the two arms, and one that is measuring container-restart noise will not. That
makes it a **calibration of the instrument** — worth running once on Docker before
trusting any cloud number.

It requires, and only Docker can provide:

- **An ungraceful kill** — `Crash`, which no managed provider offers (§1.1).
- **Pinned checkpointing** — `checkpoint_timeout=1h`, `max_wal_size=64GB` via
  `Settings` (§3.3). With defaults, pgbench triggers its own checkpoints through
  `max_wal_size` and the arms stop differing by one variable.
- **A WAL baseline reset** — `CHECKPOINT` after `pgbench -i`, before the load
  phase, since with checkpointing pinned off the init WAL is never flushed away on
  its own.
- **The independent variable recorded**, before the kill and after stopping
  `bench` so it isn't a moving target:
  ```sql
  SELECT pg_current_wal_lsn() - (SELECT redo_lsn FROM pg_control_checkpoint());
  ```
- **`redo_elapsed_ms`**, parsed from the server log — `redo done at … elapsed:
  1.23 s`, or `redo is not required` in the checkpointed arm, where NULL is a
  result rather than a missing value. Reading it needs a `provider.LogReader`
  capability that does not exist: `harness.Runner.Logs` only reaches containers the
  harness started, and the database belongs to `provider`.

Expected result: the checkpointed arm shows `redo is not required` and recovers in
container-restart time; the other shows redo time growing with `wal_bytes`. If both
arms come out flat, the prober is measuring restart noise — and the cloud numbers
it produces would have the same problem invisibly.
