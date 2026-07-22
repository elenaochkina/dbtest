# Plan: In-Region Benchmarking via ECS Fargate

Run the pgbench client **in the RDS region** so the AWS numbers reflect database
throughput, not internet latency — using a serverless container instead of a
managed EC2 box. Container evolution of `in-region-benchmark.md` (EC2 approach,
paused).

Status: design. No code yet.

> **Orchestration model.** Benchmarks run through **Temporal** — `cmd/starter`
> triggers a workflow, `cmd/worker` executes the activities. `cmd/runbenchmark`
> was the *pre-Temporal*, direct-execution harness (one process: provision →
> scenario → state, with an interactive prompt); it is superseded by
> starter + worker and is being removed. Nothing below ports `runbenchmark` into
> the container — the container is a small new binary (§5).

---

## 1. Why (the problem this solves)

The first live RDS run measured **6 tps / 652 ms** vs Docker's **2876 tps / 1.4 ms** —
a ~480× gap that is **network distance, not database power**. pgbench is
latency-bound: every statement pays a full WAN round-trip (~50–80 ms) from the
laptop to us-east-2. The measurement is invalid for comparing *databases*.

Fix: co-locate the pgbench client with RDS (same region/VPC), turning ~60 ms
round-trips into ~0.3–0.5 ms.

## 2. Why Fargate works here

The Fargate container's only job is to run **pgbench against an RDS endpoint** —
it needs the `pgbench` binary and a network path to RDS, nothing else. No Docker
daemon, so it's fully Fargate-compatible.

One boundary: the **`docker` provider baseline cannot run on Fargate** — it needs
a Docker daemon to spin up a local Postgres container, which Fargate doesn't
provide. That's fine: only the **`aws` provider** is exercised in-region; the
Docker baseline stays on the laptop as a local-latency reference.

## 3. Architecture — three layers, split by lifecycle

| Concern | Tool | Runs where | Cadence |
|---|---|---|---|
| Static infra: ECR, ECS cluster, task definition, IAM roles, SG | **Terraform** | AWS | once |
| Orchestrate a run: provision RDS, launch task, wait, fetch, persist, tear down | **Temporal** (`starter`+`worker`) | worker (**local**) | per run |
| Wait-for-ready + run pgbench against the endpoint | **thin container** | **Fargate** (in-region) | per run |
| Persist result | `SaveBenchmarkResult` | worker → **local stateDB** | per run |

```
cmd/starter ──trigger──► Temporal worker (LOCAL — has state-DB + RDS-API access)
   ├─ StartRun ──────────────────────────────────► local stateDB
   ├─ Provision RDS (rds:CreateDBInstance, API)     [defer Deprovision — durable]
   ├─ RunFargateBenchmark(DSN):
   │     ecs:RunTask ─────────────────────────────► Fargate task (in-region, thin)
   │                                                   wait-ready(DSN) → pgbench(DSN)
   │                                                   → emit JSON result
   │     poll DescribeTasks until STOPPED, fetch result
   ├─ SaveResult ────────────────────────────────► local stateDB
   ├─ (defer) Deprovision RDS (rds:DeleteDBInstance, API)
   └─ EndRun ────────────────────────────────────► local stateDB
```

## 4. The two constraints that shape this design

**(a) State-DB bridge.** A Fargate task lives in the AWS VPC; the state DB is on
`localhost:5433` behind NAT with no public inbound — **the task cannot connect
back to the laptop.** Resolution: the task emits its result; the **local worker**
(which *does* have local state-DB access) persists it. The task never touches the
state DB.

**(b) Control-plane vs data-plane split.** The local worker can do **control-plane**
work from the laptop — the RDS *API* (`CreateDBInstance`/`DeleteDBInstance`) and
the ECS *API* (`RunTask`/`DescribeTasks`) are public endpoints. But it **cannot do
data-plane** work against a **private** RDS instance — a `pgx`/`pgbench` connection
on 5432 is only reachable *inside the VPC*. So:

| Work | Plane | Where it must run |
|---|---|---|
| Provision / deprovision RDS | control (API) | **local worker** |
| Launch / poll the Fargate task | control (API) | **local worker** |
| Persist result to local stateDB | — | **local worker** |
| **Wait-for-ready, pgbench** | **data (5432)** | **Fargate** (in-VPC) |

This is *why* the container does wait-for-ready + pgbench and the worker does
everything else. Note the existing `ProviderActivities.WaitForReady` (a `pgx`
connect) **can't run on the local worker** for a private RDS — readiness moves
into the container.

## 5. The thin pgbench container binary

A new, small headless binary (e.g. `cmd/benchrunner`) — **not** a port of the
removed `runbenchmark`. Its whole job:

1. read a DSN + pgbench params from **env/flags** (no interactive prompt),
2. **wait for the DB to accept connections** (a `pgx` connect loop — RDS may not
   be ready the instant it's `available`),
3. run pgbench (`pgbench.RunLocal` = init + TPC-B run),
4. print **one JSON result line** to stdout, then exit with a status code.

No provider, no scenario engine, no state DB, no `STATE_DSN`. It's headless and
JSON-emitting by construction. **Verify locally** against any Postgres (even the
Docker one) before Dockerizing.

## 6. Networking

Split the two paths — they have different needs:

- **Data path (pgbench → RDS, 5432):** intra-VPC private. RDS SG allows 5432 from
  the task SG. Set `AWS_RDS_PUBLIC=false` + a subnet group.
- **Control path (the task's ECR pull + CloudWatch logs):** needs egress, or the
  task never starts.

Three ways to give the task egress:

| Option | Cost / setup | Notes |
|---|---|---|
| **Public subnet + `assignPublicIp=ENABLED`** | cheapest, simplest | RDS still private; task reaches ECR/Logs over the IGW. **Recommended for the first cut.** |
| Private subnet + **NAT gateway** | ~$32/mo + data | One route covers egress; wasteful for occasional runs |
| Private subnet + **VPC endpoints** | most locked-down, most setup | Needs *several*: `ecr.api`, `ecr.dkr`, `s3` (gateway), `logs` |

(Note: with the worker doing RDS provisioning from the laptop, the *task* no
longer needs RDS-API egress — only ECR + Logs. That simplifies the endpoint set.)

## 7. IAM — two distinct roles

- **Execution role** — lets Fargate pull the image from ECR and write CloudWatch
  logs. Required or the task won't start.
- **Task role** — the container only runs pgbench, so it needs **no AWS
  permissions** at all (a minimal/empty role). The RDS `rds:Create/Describe/Delete`
  permissions live with the **local worker's** credentials, not the task.

(This is a nice consequence of Option B: the in-region task carries no AWS power.)

## 8. Container image

Multi-stage, static Go binary + `pgbench`:

- Build the **`cmd/benchrunner`** target (not `.` — the repo root has no `main`).
- **Pin the builder to the `go.mod` Go version**, not a guessed tag.
- **Verify pgbench at build time:** `RUN which pgbench` after install so a missing
  binary fails the *build*. Alpine's `pgbench` packaging is unreliable; if absent,
  use `debian:bookworm-slim` + `postgresql-client`.
- `CGO_ENABLED=0` — pgx is pure Go, so the binary is static.

Push to **ECR**.

## 9. Terraform scope (once)

ECR repo, ECS cluster, task definition (image + log config + roles), the task
security group, and — if using a private subnet — NAT/VPC endpoints. Region is
**us-east-2** (where the RDS test SG lives). Idle cost of a cluster + task def
with no running tasks is ~zero. The RDS `AWS_RDS_*` config now lives with the
**worker's** environment, not the task definition.

## 10. Temporal orchestration (per run)

A new workflow (e.g. `InRegionBenchmarkWorkflow`) that **reuses existing
activities** and adds one:

```
StartRun                        → runID          (SaveResultActivities — local DB)
SideEffect                      → password
Provision RDS (private)         → cluster         (ProviderActivities — API)   [defer Deprovision]
RunFargateBenchmark(cluster DSN)→ pgbench.Result  (NEW activity: RunTask + poll + fetch result)
SaveResult(runID, result)                          (SaveResultActivities — local DB)
(defer) Deprovision RDS                            (ProviderActivities — API, durable teardown)
EndRun(passed)                                     (SaveResultActivities — local DB)
```

New code = **one activity** (`RunFargateBenchmark`: `ecs:RunTask`, poll
`DescribeTasks` to STOPPED, check exit code, fetch the result JSON) + the thin
container (§5). `StartRun` / `Provision` / `Deprovision` / `SaveResult` / `EndRun`
are the activities already built — including the **durable `defer` teardown**, so
a mid-run failure still deletes the paying RDS instance. Triggered by `cmd/starter`
(add a `-workflow in-region` case).

The worker passes the RDS DSN to the task via a **RunTask container override**
(env). The DSN carries the master password — acceptable for a throwaway task; move
to Secrets Manager if it needs hardening.

## 11. Result handoff

stdout in Fargate → **CloudWatch Logs**, so `RunFargateBenchmark` "fetches the
result" by reading the task's log stream. Scraping arbitrary lines is fragile —
have the container emit **one clean JSON envelope** (e.g. a `RESULT: {…}` marker)
so the activity grabs exactly one, or write the JSON to **S3** for a robust
handoff. Either is fine.

## 12. Validity caveats (carried from in-region-benchmark.md)

- **Localhost still has an edge.** Even intra-AZ (~0.3 ms), RDS-over-VPC won't
  match a Docker Postgres on `localhost` (~0.05 ms). Honest framing: "managed
  Postgres over the VPC" vs "local container" — both free of the WAN tax.
- **Re-tune pgbench.** Once you're not latency-bound, raise `-clients` and
  `-duration` — short/low-concurrency runs under-utilize the DB.
- **Compare like sizes.** For DB-power comparisons, use the same instance class on
  both sides.

## 13. Cleanup

- RDS is deleted by the workflow's **durable `defer` teardown** (worker-side) on
  every exit path. Verify with `describe-db-instances`.
- The Fargate task self-stops when the process exits — nothing to terminate.
- `terraform destroy` tears down the static infra when the project is done.

## 14. Build order

1. **Thin pgbench container binary** (`cmd/benchrunner`) — wait-ready + pgbench +
   JSON. Verify locally against any Postgres.
2. **Dockerfile** (+ `which pgbench` check) → build → **ECR** push; run the image
   locally against a Postgres to confirm before AWS.
3. **Terraform** static infra (cluster, task def, roles, SG, networking).
4. **`RunFargateBenchmark` activity + `InRegionBenchmarkWorkflow`** — wire into
   `worker`/`starter`; first prove the RunTask path with `aws ecs run-task`, then
   the workflow.

## 15. Open decisions

- **Result transport:** stdout/CloudWatch vs S3. §11.
- **Networking:** public-subnet+public-IP vs private+NAT vs private+endpoints. §6.
- **DSN secrecy:** plain env override vs Secrets Manager. §10.
