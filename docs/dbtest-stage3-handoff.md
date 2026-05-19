# dbtest — Stage 3: State Store

## Project context

`dbtest` is a Go framework for testing PostgreSQL databases in a reproducible way.
Module: `github.com/elenaochkina/dbtest`

## What was completed in Stage 2 (PR #2 — merged)

```
telemetry/telemetry.go      ← Prometheus registry + slog setup + HTTP /metrics server
adapter/adapter.go          ← WithMetrics option, connect duration metric + log line
benchmark/seed.go           ← rows seeded counter + log line per INSERT
validator/validator.go      ← checksum duration histogram + log line
benchmark/warehouse_test.go ← telemetry initialized at test start
```

All Stage 1 tests still pass. Metrics visible at `http://localhost:9090/metrics`
during test runs. JSON structured logs emitted to stdout.

Local Postgres running in Docker:
- Container: `dbtest-postgres`
- DSN: `postgres://postgres:test@localhost/postgres`
- Start: `docker start dbtest-postgres`

---

## Stage 3 goal

Give the framework **memory across runs**. Right now every test run starts
from scratch — seeds, checksums, and results vanish when the process exits.
Stage 3 adds a second Postgres database (the state store) that the framework
writes to. Each test run is recorded with its seed, scenario name, timestamps,
and checksum snapshots.

**What this unlocks:** run the same test tonight and tomorrow night, and on
the second run the framework automatically compares tonight's checksums against
last night's. If a single row changed that shouldn't have, the test fails and
tells you exactly which checkpoint and which table drifted.

New infrastructure needed: **one additional Postgres instance** for the state
store. The database being tested is untouched — the state store is entirely
separate.

---

## New concepts introduced in Stage 3

**State store** — a Postgres database owned by the framework itself, not the
database under test. Think of it as a logbook. Every run gets a row. Every
checkpoint gets a row. The tested database never knows the state store exists.

**Run** — one execution of a test scenario. A run record stores: the scenario
name, the seed used for data generation, start time, end time, and whether it
passed or failed. Runs are how you build a history.

**Checkpoint** — a named snapshot captured at a specific moment during a run.
Example: `"after_seed"` stores the checksum right after seeding completes.
`"after_orders"` stores the checksum after the order cycle. Checkpoints are
what you compare across runs: "did the warehouse table look the same after
seeding today as it did yesterday?"

**Auto-migrating schema** — instead of running SQL scripts by hand before
every test, the state store client creates its own tables automatically the
first time it connects. You call `state.Connect(dsn)` and the schema appears
if it doesn't exist yet. No migration tool, no manual SQL, no extra setup step.
This matters especially in CI where a fresh Postgres appears for every run.

**Historical comparison** — after loading the previous run, the validator can
compare a checkpoint from today against the same checkpoint from yesterday.
If they match: the data is stable. If they differ: something changed that
shouldn't have, and the test fails with a clear message.

---

## New local infrastructure

You need a second Docker container running Postgres for the state store.
It should be on a different port (5433) so it doesn't conflict with the
existing `dbtest-postgres` container on port 5432.

```
Container name: dbtest-state
Port:           5433 → 5432
State store DSN: postgres://postgres:test@localhost:5433/postgres
Start command:  docker start dbtest-state
```

---

## Files to create

```
state/
└── state.go        ← Store, Run, RunConfig types; Connect, StartRun, LastRun,
                      Run.Checkpoint, Run.End, Run.GetCheckpoint

pkg/checksum/
└── checksum.go     ← Checksum struct moved here (see dependency rules below)
```

## Files to modify

```
validator/validator.go      ← add AssertMatchesPrior; replace Checksum struct
                              with a type alias pointing at pkg/checksum
benchmark/warehouse_test.go ← open a run, save checkpoints, compare to previous run
```

---

## Schema (two tables in the state store)

**`runs`** — one row per test execution.

Columns: `id` (auto-incrementing primary key), `scenario` (name you choose,
e.g. `"warehouse-consistency"`), `seed` (the integer passed to `seedgen.New`
so any run can be reproduced), `provider` (hardcoded `"manual"` for now),
`started_at`, `ended_at`, `passed`.

**`checkpoints`** — one row per named snapshot within a run.

Columns: `id`, `run_id` (foreign key to `runs`), `name` (the label like
`"after_seed"`), `row_count`, `stock_sum`, `captured_at`.

Both tables use `CREATE TABLE IF NOT EXISTS` so running the migration multiple
times is always safe — it is a no-op when the tables already exist.

---

## Task 1 — `pkg/checksum/checksum.go`

A tiny new package containing only the `Checksum` struct (currently defined
in `validator`). Moving it here breaks a circular import that would otherwise
occur when `validator` imports `state` and `state` imports `validator`.

No functions, no logic — just the struct definition and a short comment
explaining what each field means.

---

## Task 2 — `state/state.go`

### Types

**`RunConfig`** — input parameters for starting a run. Fields: `Seed int64`,
`Scenario string`, `Provider string`. The scenario name must be consistent
across runs so that `LastRun` can find the previous one.

**`Store`** — the state store client. Holds an unexported pgx connection pool.
One instance is created per test binary via `Connect`.

**`Run`** — represents one active or completed test run. Exported fields: `ID int64`
and `Config RunConfig`. Has an unexported back-pointer to the `Store` so its
methods can write to the database without the caller passing the store around.

### Functions

**`Connect(dsn string) (*Store, error)`**

Opens a pgx connection pool to the state store DSN. Immediately calls the
internal `migrate` helper to create tables if they don't exist. Logs a
confirmation line. Returns an error (not a panic) if the database is
unreachable.

**`(s *Store) migrate(ctx context.Context) error`** — unexported.

Runs both `CREATE TABLE IF NOT EXISTS` statements. Called only from `Connect`.
Safe to call on every startup.

**`(s *Store) StartRun(ctx context.Context, cfg RunConfig) (*Run, error)`**

Inserts a row into `runs` with `started_at = now()` and returns a `Run`
containing the new database ID. Logs the run ID, scenario, and seed.
`ended_at` and `passed` are left NULL until `End` is called.

**`(r *Run) Checkpoint(ctx context.Context, name string, cs checksum.Checksum) error`**

Inserts a row into `checkpoints` linked to this run. `name` is the label the
caller chooses (e.g. `"after_seed"`). Logs the checkpoint name and both
checksum fields.

**`(r *Run) End(ctx context.Context, passed bool) error`**

Sets `ended_at = now()` and `passed` on the run's row. Should be called with
`passed = !t.Failed()` so the result reflects whether any assertion fired.
The right pattern is a deferred call immediately after `StartRun` — that way
it runs even if the test panics or returns early.

**`(r *Run) GetCheckpoint(ctx context.Context, name string) (checksum.Checksum, error)`**

Loads a named checkpoint from the database for this run. Used internally by
`AssertMatchesPrior` — the test itself never calls this directly. Returns an
error if the checkpoint name doesn't exist for this run.

**`(s *Store) LastRun(ctx context.Context, scenario string) (*Run, error)`**

Queries for the most recently completed, passing run of a given scenario.
Returns `nil, nil` (not an error) when no previous run exists — this is the
expected case on first run ever, and must be documented clearly in the function
comment. Returns an error only if the query itself fails.

---

## Task 3 — modify `validator/validator.go`

Replace `type Checksum struct { ... }` with a type alias:
`type Checksum = checksum.Checksum`. This keeps all existing call sites working
without changes while moving the canonical definition to `pkg/checksum`.

Add one new function:

**`AssertMatchesPrior(t *testing.T, ctx context.Context, current *state.Run, prior *state.Run, checkpointName string)`**

Loads the named checkpoint from both runs via `GetCheckpoint` and compares
them field by field. If they differ, calls `t.Errorf` with a message showing
the checkpoint name, both run IDs, and the differing values. If they match,
logs a confirmation line.

Do not change `ComputeChecksum`, `AssertDelta`, or the `Checksum` fields —
Stage 2 tests must still pass unchanged.

---

## Task 4 — modify `benchmark/warehouse_test.go`

The structure of the test grows but the existing logic does not change.

**At the top:** connect to the state store using `STATE_DSN` from the
environment. If `STATE_DSN` is not set, skip state tracking entirely — all
state store calls are guarded with `if ss != nil` / `if run != nil`. This
keeps the test runnable without the second Postgres for quick local iteration.

**After connecting:** call `StartRun` with seed 42, scenario
`"warehouse-consistency"`, provider `"manual"`. Register a deferred `End`
call immediately.

**After computing `before` checksum:** save a checkpoint named `"after_seed"`.

**After computing `after` checksum:** save a checkpoint named `"after_orders"`.

**After `AssertDelta`:** call `LastRun` for the scenario. If a previous run is
found and its ID differs from the current run's ID, call `AssertMatchesPrior`
for both checkpoints. The ID check prevents a run from comparing itself against
itself when the test binary is reused within a session.

---

## Run all tests

**First run — no history yet:**
```bash
DSN="postgres://postgres:test@localhost/postgres" \
STATE_DSN="postgres://postgres:test@localhost:5433/postgres" \
go test ./... -v
```

`LastRun` returns nil. The prior-run comparison is skipped. The run is written
to the state store and marked passed.

**Second run — comparison active:**

Same command. `LastRun` returns the first run. `AssertMatchesPrior` compares
both checkpoints — both should pass since nothing changed.

**Without state store:**
```bash
DSN="postgres://postgres:test@localhost/postgres" go test ./... -v
```

Behaves exactly as Stage 2. All existing tests pass.

---

## Verify state store contents

After running the test you can inspect the state store directly:

```bash
docker exec -it dbtest-state psql -U postgres -c "SELECT * FROM runs;"
docker exec -it dbtest-state psql -U postgres -c "SELECT * FROM checkpoints;"
```

After two runs you should see 2 rows in `runs` (both `passed = true`) and
4 rows in `checkpoints` (2 checkpoints × 2 runs), with identical `row_count`
and `stock_sum` values across both runs.

---

## Expected log output (second run)

```
state store connected
run started         run_id=2  scenario=warehouse-consistency  seed=42
connected to database
seeded row  table=warehouse  id=1
...
computed checksum   table=warehouse  row_count=5  stock_sum=25053
checkpoint saved    run_id=2  name=after_seed    row_count=5  stock_sum=25053
computed checksum   table=warehouse  row_count=5  stock_sum=25043
checkpoint saved    run_id=2  name=after_orders  row_count=5  stock_sum=25043
checkpoint matches prior run  checkpoint=after_seed    current_run_id=2  prior_run_id=1
checkpoint matches prior run  checkpoint=after_orders  current_run_id=2  prior_run_id=1
run ended  run_id=2  passed=true
```

---

## Package dependency rules

```
pkg/checksum/  ← Checksum struct only; imports nothing from this project
telemetry/     ← imports only stdlib + prometheus client
state/         ← imports pkg/checksum, telemetry, pgx
adapter/       ← imports telemetry
validator/     ← imports pkg/checksum, state, telemetry
benchmark/     ← imports adapter, pkg/seedgen, validator, state, telemetry
```

**Why `pkg/checksum` is new:** `state` needs the `Checksum` type to store and
retrieve checkpoints. `validator` needs the `state.Run` type to implement
`AssertMatchesPrior`. If each package imported the other, Go would refuse to
compile — circular imports are forbidden. Moving `Checksum` to a neutral shared
package that both can import breaks the cycle cleanly.

No new Go dependencies are needed. `state` uses the same `pgxpool` package
already present from Stage 1.

---

## Notes for the agent

- The author is a beginner — add a comment to every function explaining what
  it does, what each parameter means, and what the caller should do with the
  result
- `STATE_DSN` being unset is not an error — guard every state store call with
  nil checks and document this behaviour in the test comments
- `LastRun` returning `nil, nil` is the normal first-run case — make this
  explicit in the function comment so it is not mistaken for a bug
- Auto-migration must be idempotent: `CREATE TABLE IF NOT EXISTS` is the
  correct form; never use `CREATE TABLE`
- Use `context.Context` as the first parameter on every new function,
  consistent with existing code
- Do not change `AssertDelta`, `ComputeChecksum`, or the `Checksum` fields