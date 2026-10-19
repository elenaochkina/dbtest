# dbtest console — interactive TUI for the Temporal path

**Status:** proposed design, 2026-08-14; updated 2026-10-18 against `main` at `ffc06ed`. Not yet
implemented.

## Context

Running the durable (Temporal) path today needs three terminals plus tribal knowledge: install
the `temporal` CLI (not installed on a fresh machine), start a state Postgres on a port that
isn't taken, export `STATE_DSN`, run `cmd/worker`, then run `cmd/starter` with the right flags
for one of two workflows (`pgbench`, `recovery`). Both `cmd/worker` and `cmd/runbenchmark`
hardcode metrics port 9090 and fight if both run.

On AWS there are more steps, all done by hand today:

- sign in with IAM Identity Center (`aws sso login`) and pick the `dbtest` profile, which
  assumes the role created by `terraform/bootstrap`;
- generate `.env` from `terraform/aws` (`output -raw worker_env`) and `source` it, so the worker
  has `AWS_REGION`, `AWS_RDS_*` and `AWS_ECS_*`;
- push the probe and bench images to ECR for Fargate runs (`make push`).

The SSO session lasts about an hour. When it expires mid-run, `StartProbe` and `Deprovision`
exhaust their retries within seconds and leave a billable cluster behind (the TODO above
`Deprovision` in `temporal/activities/provider_activities.go:73`). And the open TODO at
`provider/aws/rds.go:81` still means a public RDS instance with no `AWS_RDS_SECURITY_GROUP_IDS`
is **billable but unreachable**, then burns 5 minutes in `WaitForReady`.

Instead of a one-shot launcher, build a **stateful interactive console**: it brings the
components up, then presents a persistent three-panel view — component status, state-dependent
actions, and live logs from the active run. Existing binaries (`cmd/worker`, `cmd/starter`,
`cmd/runbenchmark`) keep working unchanged so CI and scripting are unaffected.

Decisions already made: **Bubble Tea + Lipgloss + Bubbles**; **three stacked full-width rows**;
the worker sits **behind an interface** with an in-process implementation first.

"Provider" here means a **managed Postgres provider**, and the list is expected to grow. Today
three are registered: `docker` (the local zero-cost baseline), `rds` (RDS PostgreSQL, single or
Multi-AZ) and `aurora` (Aurora PostgreSQL). **GCP Cloud SQL and Azure Database for PostgreSQL
Flexible Server** come next. Nothing in the console may hardcode a provider set — see
[Provider set and the config seam](#provider-set-and-the-config-seam).

## UX

```
┌ STATUS ────────────────────────────────────────────────────────────┐
│ state db   ● up    postgres://…@localhost:54321/postgres           │
│ temporal   ● up    localhost:7233   UI http://localhost:8233       │
│ telemetry  ● up    http://localhost:53411/metrics                  │
│ providers  1 ready (docker)   2 need login (rds, aurora)           │
│ runners    docker ● up   fargate ○ needs .env                      │
├─ ACTIONS ──────────────────────────────────────────────────────────┤
│ > 1 start pgbench    2 start recovery    3 configure providers     │
│   4 list resources   5 past workflows    6 exit                    │
├─ LOG  (active run: recovery-20261018-142530) ──────────────────────┤
│ 14:25:31 provisioned cluster dbtest-3f9a…                          │
│ 14:25:33 cluster is ready                                          │
│ 14:26:10 disruption 1/3 restart applied                            │
│ 14:26:19 disruption 1/3 writable again after 8.4 s                 │
└────────────────────────────────────────────────────────────────────┘
```

Keys: `↑↓`/`1-9` select, `⏎` confirm, `esc` back/cancel, `tab` focus log pane for scrollback,
`r` retry a failed component, `?` help overlay, `q` quit.

**Actions are derived from state, never hardcoded** — `Model.actions()` is a pure function of
component and provider state, so: both "start" actions are disabled until state db + temporal are
up; a cloud provider is offered only once configured; "start recovery" needs a container runner
for the chosen provider (docker for `docker`, fargate for `rds`/`aurora`); "cancel run" replaces
the start actions while a run is active. This is the piece to unit-test hardest.

**Quitting with an active run is two-stage** (the highest-value safety behavior): first `q`
confirms, then cancels the workflow and stays up while the deferred teardown runs, showing
progress. Teardown is `Deprovision` (`pgbench_workflow.go:63`, `recovery_workflow.go:89`) and,
for recovery, `StopProbe` (`recovery_workflow.go:142`). A second `q` force-exits with a loud
warning naming the cluster ID that may still bill.

## Architecture

Rule: `internal/console` never imports the docker, Temporal, AWS or pgx SDKs. It talks to the
engine through interfaces and receives everything as messages, so the whole UI is testable with
fakes and no terminal.

```
cmd/dbtest/main.go              flags, slog redirect, tea.NewProgram
internal/console/               Bubble Tea (UI only)
  model.go                      root Model, Init/Update/View, nav stack ([]screen; esc = pop)
  msgs.go                       the event vocabulary (all tea.Msg types)
  keys.go  theme.go             keymap; lipgloss styles
  panel_status.go               STATUS row
  panel_actions.go              ACTIONS row + Model.actions() gating
  panel_log.go                  LOG row: viewport + ring buffer
  screen_root.go  screen_pgbench.go  screen_recovery.go  screen_providers.go
  screen_resources.go  screen_history.go
internal/stack/                 engine the console drives (no UI imports)
  supervisor.go                 Component interface + state machine + health polling
  statedb.go  temporalsrv.go    managed containers via the docker SDK
  telemetry.go                  metrics server component
  ports.go  config.go           free-port picker; .dbtest.local.json
  resources.go  history.go      leaked-resource listing; past runs
internal/workerhost/            THE SEAM
  workerhost.go                 interface + LogLine type
  inprocess.go                  v1 implementation (goroutine)
internal/logbus/logbus.go       slog.Handler → batched LogLine stream + ring buffer
internal/providercfg/           per-provider config + credential flows
  configurator.go               the interface; lookup by provider name
  aws.go                        SSO login; dbtest role; .env; reachability; cost preview
  (gcp.go, azure.go)            later — one file per cloud, no console changes
temporal/worker.go              NEW shared registration list
```

**The worker seam** — swapping to a child process or container later touches no console code:

```go
type WorkerHost interface {
    Start(ctx context.Context) error     // begin polling the task queue
    Stop(ctx context.Context) error      // graceful drain
    Health() stack.State
    Logs() <-chan logbus.LogLine         // structured lines, however they were produced
}
```

`LogLine{Time, Level, Msg, Attrs, Source}` is the common currency: the in-process host feeds it
straight from an `slog.Handler`; a future child-process host parses the worker's JSON stdout into
the same type; a container host would tail container logs. The console only ever sees the channel.

The in-process host must register what `cmd/worker` registers today: the providers and container
runners through blank imports (`cmd/worker/main.go:13-19`), and both workflows plus five activity
structs (`cmd/worker/main.go:59-65`). `temporal/worker.go` holds that list once for both.

**Component supervisor** — each of state db / temporal / telemetry implements one interface with
states `unknown → starting → up → error`, emitting `componentStateMsg` on every transition so the
STATUS panel is live rather than polled once at boot. Containers are **adopted, not recreated**:
names `dbtest-state`/`dbtest-temporal`, labels `dbtest.managed=true` + `dbtest.role=…`; running →
adopt its published port, stopped → start, absent → create.

## Provider set and the config seam

The roadmap is `docker` + **AWS (RDS, Aurora) → GCP Cloud SQL → Azure Database for PostgreSQL
Flexible Server**, so the provider list only grows. Three consequences:

**The console enumerates, never hardcodes.** `provider/provider.go:75-96` already has the
self-registering registry (`Register`, `Run`, `registeredNames()`); the STATUS row and the
providers screen render from it. `harness/harness.go` has the same shape for container runners
(`docker`, `fargate`). The status row shows counts plus names and collapses to `… +N more` once
it overflows the width; full per-provider detail lives in the providers screen, which is a
scrollable table rather than a fixed list.

**Config, credentials and cost preview become optional capability interfaces**, checked with a
type assertion like any optional Go interface — so a new cloud is a new file under `provider/`
plus one under `internal/providercfg/`, with no console change:

```go
// implemented per provider; the console only ever knows this interface
type Configurator interface {
    Fields() []Field                      // what to prompt for (profile, project, subscription…)
    Validate(ctx context.Context) Status   // ready | needs-login | misconfigured + reason
    Login(ctx context.Context) error       // aws sso login / gcloud auth ADC / az login
}

type SizingPreview interface {            // optional: what a ProvisionRequest will actually create
    Preview(req provider.ProvisionRequest) (string, error)
}
```

Per-cloud specifics live behind that interface: **AWS** — SSO profile, the `dbtest` role,
`.env` from `terraform/aws`, instance class from the sizing tables; **GCP** — project + region +
ADC via `gcloud auth application-default login`, machine type; **Azure** — subscription +
resource group via `az login`, SKU tier. `.dbtest.local.json` carries one section per provider,
so configuring one cloud never disturbs another. `rds` and `aurora` share one AWS section.

**Capabilities gate the action list.** Every provider implements
`Supports(req, disruption) bool` (`provider/provider.go:53-55`), and the recovery screen offers
only the disruptions it accepts for the chosen request:

| Provider | restart | crash | failover |
|---|---|---|---|
| `docker` | ✓ | ✓ | — |
| `rds` | ✓ | — | with `-ha` |
| `aurora` | ✓ | ✓ (fault injection) | with `-ha` |

`Supports` is a method on a constructed provider, and constructing one loads its config, so the
console calls it once a provider is configured, not at startup. As each cloud gains a
disruption, the option appears through the same `Model.actions()` derivation rather than a new
conditional in the UI.

## Engine decisions

- **Temporal server** = `temporalio/temporal:1.8.2` container running `server start-dev --ip
  0.0.0.0 --ui-ip 0.0.0.0 --port <P> --ui-port <P+1000> --db-filename /data/temporal.db` (tag
  confirmed on Docker Hub 2026-07-31; flags against docs.temporal.io/cli/server). Publish
  **identical inside/outside ports** (`P:P`) so no advertise-address mismatch is possible; named
  volume for `/data` so history survives. *Verify at build time:* `docker run --rm
  temporalio/temporal:1.8.2 server start-dev --help`. Fallbacks: `temporalio/auto-setup` against
  the state Postgres, or fail with `brew install temporal`.
- **Worker stays on the host.** `provider/docker/docker.go:321` hardcodes `Host: "localhost"`, so
  a containerized worker would dial itself. The pgbench workflow runs `pgbench` from the worker's
  PATH (`pgbench/runner.go`); the recovery workflow runs its probe and bench in containers
  through the harness runner instead.
- **Recovery images must exist before a run.** Docker runs need `dbtest/probe:dev` and
  `dbtest/bench:dev` built locally (`make images`); Fargate runs need them pushed to ECR
  (`make push`) and take the task definition names (`dbtest-probe`, `dbtest-bench`) as images.
  The console checks for them and offers the make target rather than failing mid-workflow.
- **No port is assumed.** Probe and cache every port; treat `MetricsPort: 0` as auto-pick and make
  `telemetry.InitMetrics` return its bind error instead of swallowing it into a warning
  (`telemetry/metrics.go:102-103`). Port 9090 is hardcoded in `cmd/worker/main.go:31` and
  `cmd/runbenchmark/main.go:32`, and 5433–5435 are commonly already occupied by other local
  Postgres containers.
- **stdout is off-limits.** `cmd/*` call `slog.Info`/`slog.Error` on the default logger, which
  would paint over the TUI, so `main` must `slog.SetDefault` to the logbus handler before starting
  the program. (`telemetry/logs.go:15` says `InitLogging` installs a global default; it doesn't.)
- **Log volume is real.** `benchmark/seed.go:39` logs one line *per seeded row*. Batch lines
  (drain every ~75 ms into one `logBatchMsg`) and cap the ring at ~2000, or a seed of any size
  floods `Update` and the viewport re-renders per line.
- **Cloud config flow — AWS is the reference implementation** of `Configurator`, and every later
  cloud repeats its shape: establish identity → scope it (region / project / resource group) →
  guard reachability → preview cost → confirm. For AWS, all before anything billable:
  1. **Identity.** Profile from `-aws-profile` → `AWS_PROFILE` → saved config → a
     substring-filtered prompt. The expected choice is `dbtest`, which assumes the role
     through the user's SSO profile; show the assumed-role ARN from `sts:GetCallerIdentity`
     (promote `aws-sdk-go-v2/service/sts` to a direct dep). On an expired SSO token, prompt, run
     `aws sso login --profile <source profile>`, retry once.
  2. **Session length.** Warn before a run when the SSO token expires sooner than the run's
     expected length, since teardown needs valid credentials.
  3. **Scope.** Region and the static infrastructure: load `.env` (or run
     `terraform -chdir=terraform/aws output -raw worker_env`) and show which of `AWS_RDS_*` and
     `AWS_ECS_*` are set. Missing → point at `terraform/README.md`; the console does not apply
     Terraform.
  4. **Reachability guard.** Public + no `AWS_RDS_SECURITY_GROUP_IDS` = stop here, per
     `rds.go:81`.
  5. **Cost preview** from exported `resolveInstanceClass` / `allocatedStorageGiB`
     (`rds.go:160`, `rds.go:184`) and `resolveAuroraClass` (`aurora.go:212`), with typed
     confirmation. A Multi-AZ RDS or a two-instance Aurora cluster is shown as two instances.

  Auto-creating security groups is deliberately out of v1 — it mutates the user's account.

## Implementation steps

**Step 0 — minimum scaffolding.** Add the three deps; `cmd/dbtest` starts a Bubble Tea program
rendering the three-panel frame from a `Model` populated with **stub** state; number/arrow
selection moves the cursor; a ticker pushes fake log lines through the real `logBatchMsg` path;
`q` quits; resize works. No docker, no Temporal, no DB. Proves layout, keys, resize and the
message plumbing. ~150 lines.

**Step 1 — engine seams + live status.** Extract `temporal.NewWorker` (registration list
currently only in `cmd/worker/main.go:59-65`); telemetry auto-port + surfaced bind error + writer
wiring; export the AWS sizing helpers. Then `stack.Supervisor` with the three real components,
driving the STATUS panel and `r`-to-retry.

**Step 2 — logs + worker.** `logbus` slog handler; `workerhost.WorkerHost` with the in-process
implementation; real log lines in the LOG panel; worker health in STATUS.

**Step 3 — start workflows.** Two form screens (`bubbles/textinput`) mirroring `cmd/starter`'s
flags. **pgbench:** provider, vCPU, memory, disk, Postgres version (default 18), seed,
warehouses, scale, clients, duration. **recovery:** the same sizing plus disruption, repetitions,
settle, `-ha`, probe/bench images and probe timings. Timestamped workflow IDs
(`<workflow>-<yyyymmdd-hhmmss>` — the current default at `cmd/starter/main.go:46-48` reuses the
bare workflow name and collides on rerun), active-run header, completion/failure, and two-stage
cancel-and-quit.

**Step 4 — providers.** Providers and runners rows in STATUS rendered from the registries;
`configure providers` screen listing every registered provider; the
`Configurator`/`SizingPreview` interfaces with **AWS as the first implementation** (one section
for `rds` and `aurora`), persisted per provider to a gitignored `.dbtest.local.json`. Cloud SQL
and Flexible Server then land as added files, not console edits.

**Step 5 — resources + history.** `list resources`: containers by label, RDS instances and
Aurora clusters by the `dbtest=true` tag (`rds.go:68`, `aurora.go:101`), running Fargate tasks
in the `dbtest` cluster, and the `clusters` table — with a confirmed delete. `past workflows`:
`runs` joined with `benchmark_results` (pgbench) and `downtime_results` (recovery, one row per
disruption) from the state DB, plus Temporal `ListWorkflow` status.

**Step 6 — polish.** README quickstart rewrite (the current one documents only "Phase 1" and
predates providers, the state DB and Temporal), help overlay, log filter/scrollback.

## Files touched outside the new folders

| File | Change |
|---|---|
| `temporal/worker.go` | new — shared registration used by `cmd/worker` and the in-process host |
| `cmd/worker/main.go` | reduce to client dial + `temporal.NewWorker` |
| `telemetry/metrics.go` | port 0 = auto; return bind error |
| `telemetry/logs.go` | fix the stale "installs global default" comment |
| `provider/provider.go` | add `Configurator` / `SizingPreview` optional capability interfaces |
| `provider/aws/aws.go`, `rds.go`, `aurora.go` | implement them — first managed provider; export the sizing helpers |
| `go.mod` | add bubbletea/lipgloss/bubbles; `service/sts` indirect → direct |
| `.gitignore` | add `.dbtest.local.json`, `.dbtest/` |
| `README.md` | quickstart rewrite around the console |

## Verification

Per step, and cumulative at the end:

```bash
go build ./... && go vet ./...
go test ./internal/console/...            # Update() is pure: state → available actions
go run ./cmd/dbtest                       # step 0: frame renders, resize, q quits
```

- **Step 0/1:** nothing may reach stdout while the program runs (redirect `os.Stdout` in a test
  and assert empty); shrink the terminal to ~60×15 and confirm no panic or overlap.
- **Step 1:** kill `dbtest-state` from another shell → STATUS flips to `error` within a poll
  interval and `r` recovers it. Hold listeners on 7233 and 5433 → the console picks other ports.
- **Step 2/3:** run pgbench end to end; assert via
  `psql "$STATE_DSN" -c 'select scenario,passed from runs order by started_at desc limit 3'` and a
  fresh `benchmark_results` row. Then a docker recovery run with `-disruption crash
  -repetitions 2` → two `downtime_results` rows for the run.
- **Quit safety:** quit mid-run → provisioned container gone (`docker ps -a`), the probe
  container stopped, and the `runs` row reads `passed = false`.
- **Durability demo (the whole point):** `kill -9` the console mid-run, relaunch → the new
  in-process worker rejoins the still-live workflow in the Temporal container and completes the
  deferred teardown. Assert no leftover cluster.
- **Step 4, no cost:** with `AWS_PROFILE` unset → guidance; expired SSO → login prompt then
  success; `.env` missing → pointer to `terraform/README.md`; public + no SG → refusal *before*
  any create, confirmed by `aws rds describe-db-instances` showing nothing new.
- **Step 5:** leak a container by hand, confirm `list resources` finds it and the delete works.

## Out of scope

- A slower retry policy for `Deprovision` (the TODO at `provider_activities.go:73`) — the console
  warns about short SSO sessions, but the fix belongs in the workflows.
- Child-process and container `WorkerHost` implementations — the interface exists for them; only
  the in-process one ships.
- Applying Terraform from the console (`terraform/bootstrap` needs an admin; `terraform/aws`
  needs the `dbtest` role's `dbtest-infra` policy).
- Auto-creating AWS security groups; the EC2 in-region harness (`docs/in-region-benchmark.md`).
- The GCP Cloud SQL and Azure Flexible Server **provider implementations** themselves — separate
  work. This plan only guarantees the console, config and capability seams don't assume AWS.
- A headless/CI mode for `cmd/dbtest` — `cmd/starter` and `cmd/runbenchmark` already cover that.
- Retiring the non-durable `scenario.Run` path.
