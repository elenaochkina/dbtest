# dbtest console — interactive TUI for the Temporal path

**Status:** proposed design, 2026-08-14. Not yet implemented.

## Context

Running the durable (Temporal) path today needs three terminals plus tribal knowledge: install
the `temporal` CLI (not installed on a fresh machine), start a state Postgres on a port that
isn't taken, export `STATE_DSN`, run `cmd/worker`, then run `cmd/starter`. Both `cmd/worker` and
`cmd/runbenchmark` hardcode metrics port 9090 and fight if both run. On AWS it's worse: no
`AWS_PROFILE`/`AWS_REGION` set, hundreds of SSO profiles to pick from, and the open TODO at
`provider/aws/aws.go:126` where the default public path creates a **billable but unreachable**
RDS instance that then burns 5 minutes in `WaitForReady`.

Instead of a one-shot launcher, build a **stateful interactive console**: it brings the
components up, then presents a persistent three-panel view — component status, state-dependent
actions, and live logs from the active run. Existing binaries (`cmd/worker`, `cmd/starter`,
`cmd/runbenchmark`) keep working unchanged so CI and scripting are unaffected.

Decisions already made: **Bubble Tea + Lipgloss + Bubbles**; **three stacked full-width rows**;
the worker sits **behind an interface** with an in-process implementation first.

"Provider" here means a **managed Postgres provider**, and the list is expected to grow: **AWS RDS
is the starting point, then GCP Cloud SQL, then Azure Database for PostgreSQL Flexible Server**,
with `docker` staying as the local zero-cost baseline. Nothing in the console may hardcode a
provider set — see [Provider set and the config seam](#provider-set-and-the-config-seam).

## UX

```
┌ STATUS ────────────────────────────────────────────────────────────┐
│ state db   ● up    postgres://…@localhost:54321/postgres           │
│ temporal   ● up    localhost:7233   UI http://localhost:8233       │
│ telemetry  ● up    http://localhost:53411/metrics                  │
│ providers  1 ready (docker)   3 need config (aws, gcp, azure)      │
├─ ACTIONS ──────────────────────────────────────────────────────────┤
│ > 1 start workflow    2 configure providers    3 list resources    │
│   4 past workflows    5 exit                                       │
├─ LOG  (active run: pgbench-20260814-142530) ───────────────────────┤
│ 14:25:31 provisioned cluster 3f9a…                                 │
│ 14:25:33 cluster is ready                                          │
│ 14:25:41 pgbench complete tps=812.4 latency_avg_ms=4.9             │
└────────────────────────────────────────────────────────────────────┘
```

Keys: `↑↓`/`1-9` select, `⏎` confirm, `esc` back/cancel, `tab` focus log pane for scrollback,
`r` retry a failed component, `?` help overlay, `q` quit.

**Actions are derived from state, never hardcoded** — `Model.actions()` is a pure function of
component and provider state, so: "start workflow" is disabled until state db + temporal are up;
the aws provider is offered only once configured; "cancel run" replaces "start workflow" while a
run is active. This is the piece to unit-test hardest.

**Quitting with an active run is two-stage** (the highest-value safety behavior): first `q`
confirms, then cancels the workflow and stays up while the deferred `Deprovision`
(`pgbench_workflow.go:69`) runs, showing progress; a second `q` force-exits with a loud warning
naming the cluster ID that may still bill.

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
  screen_root.go  screen_startworkflow.go  screen_providers.go
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
  aws.go                        profile/STS/sso login; region; reachability; cost preview
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

**Component supervisor** — each of state db / temporal / telemetry implements one interface with
states `unknown → starting → up → error`, emitting `componentStateMsg` on every transition so the
STATUS panel is live rather than polled once at boot. Containers are **adopted, not recreated**:
names `dbtest-state`/`dbtest-temporal`, labels `dbtest.managed=true` + `dbtest.role=…`; running →
adopt its published port, stopped → start, absent → create.

## Provider set and the config seam

The roadmap is **AWS RDS → GCP Cloud SQL → Azure Database for PostgreSQL Flexible Server**, with
`docker` as the local zero-cost baseline, so the provider list only grows. Three consequences:

**The console enumerates, never hardcodes.** `provider/provider.go:76-101` already has the
self-registering registry (`Register`, `Run`, `registeredNames()`); the STATUS row and the
providers screen render from it. The status row shows counts plus names and collapses to
`… +N more` once it overflows the width; full per-provider detail lives in the providers screen,
which is a scrollable table rather than a fixed list.

**Config, credentials and cost preview become optional capability interfaces**, mirroring the
existing `FailureInjector` pattern (`provider/provider.go:62`) — so a new cloud is a new file
under `provider/` plus one under `internal/providercfg/`, with no console change:

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

Per-cloud specifics live behind that interface: **AWS** — profile + `sts:GetCallerIdentity` +
`aws sso login`, instance class from the sizing table; **GCP** — project + region + ADC via
`gcloud auth application-default login`, machine type; **Azure** — subscription + resource group
via `az login`, SKU tier. `.dbtest.local.json` carries one section per provider, so configuring
one cloud never disturbs another.

**Capabilities gate the action list.** `crash-recovery` is offered only for providers implementing
`FailureInjector` — today just `docker`, since `provider/aws/aws.go` has no `KillProcess`. As each
cloud gains forced-failover support the action appears for it automatically, through the same
`Model.actions()` derivation described above rather than a new conditional in the UI.

## Engine decisions

- **Temporal server** = `temporalio/temporal:1.8.2` container running `server start-dev --ip
  0.0.0.0 --ui-ip 0.0.0.0 --port <P> --ui-port <P+1000> --db-filename /data/temporal.db` (tag
  confirmed on Docker Hub 2026-07-31; flags against docs.temporal.io/cli/server). Publish
  **identical inside/outside ports** (`P:P`) so no advertise-address mismatch is possible; named
  volume for `/data` so history survives. *Verify at build time:* `docker run --rm
  temporalio/temporal:1.8.2 server start-dev --help`. Fallbacks: `temporalio/auto-setup` against
  the state Postgres, or fail with `brew install temporal`.
- **Worker stays on the host.** `provider/docker/docker.go:242` hardcodes `Host: "localhost"`, so
  a containerized worker would dial itself; `pgbench` must also be on the worker's PATH.
- **No port is assumed.** Probe and cache every port; treat `MetricsPort: 0` as auto-pick and make
  `telemetry.InitMetrics` return its bind error instead of swallowing it into a warning
  (`telemetry/metrics.go:102`). Port 9090 is hardcoded in two binaries today, and 5433–5435 are
  commonly already occupied by other local Postgres containers.
- **stdout is off-limits.** `cmd/*` call `slog.Info`/`slog.Error` on the default logger, which
  would paint over the TUI, so `main` must `slog.SetDefault` to the logbus handler before starting
  the program. (`telemetry/logs.go`'s comment claims it installs a global default; it doesn't.)
- **Log volume is real.** `benchmark/seed.go:39` logs one line *per seeded row*. Batch lines
  (drain every ~75 ms into one `logBatchMsg`) and cap the ring at ~2000, or a seed of any size
  floods `Update` and the viewport re-renders per line.
- **Cloud config flow — AWS is the reference implementation** of `Configurator`, and every later
  cloud repeats its shape: establish identity → scope it (region / project / resource group) →
  guard reachability → preview cost → confirm. For AWS, all before anything billable: profile
  (`-aws-profile` → `AWS_PROFILE` →
  saved config → substring-filtered prompt, since a several-hundred-entry list is useless) →
  validate with `sts:GetCallerIdentity` (promote `aws-sdk-go-v2/service/sts` to a direct dep) → on
  expired SSO, prompt then `aws sso login --profile|--sso-session`, retry once → region →
  **reachability guard** (public + no `AWS_RDS_SECURITY_GROUP_IDS` = stop here, per `aws.go:126`)
  → cost preview from exported `resolveInstanceClass`/`allocatedStorageGiB` with typed
  confirmation. Auto-creating security groups is deliberately out of v1 — it mutates the user's
  account.

## Implementation steps

**Step 0 — minimum scaffolding.** Add the three deps; `cmd/dbtest` starts a Bubble Tea program
rendering the three-panel frame from a `Model` populated with **stub** state; number/arrow
selection moves the cursor; a ticker pushes fake log lines through the real `logBatchMsg` path;
`q` quits; resize works. No docker, no Temporal, no DB. Proves layout, keys, resize and the
message plumbing. ~150 lines.

**Step 1 — engine seams + live status.** Extract `temporal.NewWorker` (registration list
currently only in `cmd/worker/main.go:52-58`); telemetry auto-port + surfaced bind error + writer
wiring; export the AWS sizing helpers; fix the corrupted format string `"provshow me ider %q: %w"`
at `temporal/provider_activities.go:46`. Then `stack.Supervisor` with the three real components,
driving the STATUS panel and `r`-to-retry.

**Step 2 — logs + worker.** `logbus` slog handler; `workerhost.WorkerHost` with the in-process
implementation; real log lines in the LOG panel; worker health in STATUS.

**Step 3 — start workflow.** Form screen (`bubbles/textinput`) for workflow + params mirroring
`cmd/starter`'s flags, timestamped workflow IDs (`pgbench-<yyyymmdd-hhmmss>` — the current default
reuses the bare workflow name and collides on rerun), active-run header, completion/failure, and
two-stage cancel-and-quit.

**Step 4 — providers.** Providers row in STATUS rendered from the registry; `configure providers`
screen listing every registered provider; the `Configurator`/`SizingPreview` interfaces with
**AWS RDS as the first implementation**, persisted per-provider to a gitignored
`.dbtest.local.json`. Cloud SQL and Flexible Server then land as added files, not console edits.

**Step 5 — resources + history.** `list resources`: containers by label, RDS instances by the
`dbtest=true` tag (`aws.go:112`), and the `clusters` table — with a confirmed delete. `past
workflows`: `runs` + `benchmark_results` from the state DB, joined with Temporal `ListWorkflow`
status.

**Step 6 — polish.** README quickstart rewrite (the current one documents only "Phase 1" and
predates providers, the state DB and Temporal), help overlay, log filter/scrollback.

## Files touched outside the new folders

| File | Change |
|---|---|
| `temporal/worker.go` | new — shared registration used by `cmd/worker` and the in-process host |
| `cmd/worker/main.go` | reduce to client dial + `temporal.NewWorker` |
| `temporal/provider_activities.go` | fix format-string typo (line 46) |
| `telemetry/metrics.go` | port 0 = auto; return bind error |
| `telemetry/logs.go` | fix the stale "installs global default" comment |
| `provider/provider.go` | add `Configurator` / `SizingPreview` optional capability interfaces |
| `provider/aws/aws.go` | implement them — first managed provider; export the sizing helpers |
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
  fresh `benchmark_results` row. Then crash-recovery — note it compares fingerprints in workflow
  memory (`crash_recovery_workflow.go:126`) and writes **no** `fingerprints` rows, so verify by
  workflow success and log lines, not SQL.
- **Quit safety:** quit mid-run → provisioned container gone (`docker ps -a`) and the `runs` row
  reads `passed = false`.
- **Durability demo (the whole point):** `kill -9` the console mid-run, relaunch → the new
  in-process worker rejoins the still-live workflow in the Temporal container and completes the
  deferred teardown. Assert no leftover cluster.
- **Step 4, no cost:** with `AWS_PROFILE` unset → guidance; expired SSO → login prompt then
  success; public + no SG → refusal *before* any create, confirmed by `aws rds
  describe-db-instances` showing nothing new.
- **Step 5:** leak a container by hand, confirm `list resources` finds it and the delete works.

## Out of scope

- `docs/bugsToWatch.md` #1 (a panic records `passed = true`) — real, adjacent, separate fix.
- Child-process and container `WorkerHost` implementations — the interface exists for them; only
  the in-process one ships.
- Auto-creating AWS security groups; the EC2 in-region harness (`docs/in-region-benchmark.md`).
- The GCP Cloud SQL and Azure Flexible Server **provider implementations** themselves — separate
  work. This plan only guarantees the console, config and capability seams don't assume AWS.
- A headless/CI mode for `cmd/dbtest` — `cmd/starter` and `cmd/runbenchmark` already cover that.
- Retiring the non-durable `scenario.Run` path.
