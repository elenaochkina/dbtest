# dbtest — Stage 4: External Benchmarks as Subprocesses

## Project context

`dbtest` is a Go framework for testing PostgreSQL databases in a reproducible way.
Module: `github.com/elenaochkina/dbtest`

## What was completed in Stages 1–3

```
adapter/adapter.go          ← Connect(dsn, tel) opens pgx connection, emits metrics
adapter/adapter_test.go     ← integration test, passes green
pkg/seedgen/seedgen.go      ← deterministic RNG wrapper (math/rand/v2, PCG source)
benchmark/seed.go           ← CreateWarehouseTable, SeedWarehouses, DropWarehouseTable
validator/validator.go      ← ComputeChecksum, AssertDelta
benchmark/warehouse_test.go ← full seed → act → verify loop, all tests pass
telemetry/telemetry.go      ← Prometheus registry + slog + /metrics HTTP server
state/state.go              ← package-level functions: Connect, StartRun, Checkpoint, LastRun
```

Verified ground truth with seed(42):
- StockSum before order cycle: 25053
- StockSum after order cycle:  25043  (delta = -10)

Local Postgres (target DB):
- Container: `dbtest-postgres`
- DSN: `postgres://postgres:test@localhost/postgres`
- Start: `docker start dbtest-postgres`

State store Postgres (added in Stage 3):
- Container: `dbtest-state`
- DSN: `postgres://postgres:test@localhost:5433/postgres`
- Start: `docker start dbtest-state`

---

## Stage 4 goal

Run pgbench against the target database, parse its output into a structured
result, and persist that result in the state store for regression tracking.

pgbench is an industry-standard tool — its TPS and latency numbers are
independently verified and comparable across teams and databases. This is
different from the hand-written warehouse test in Stages 1–3, which measures
correctness. pgbench measures **performance under load**. The two tools answer
different questions and will be combined in Stage 7.

**The three phases of a pgbench run:**

1. **Initialize** (`pgbench -i`) — pgbench creates its own tables in the target
   database (`pgbench_accounts`, `pgbench_branches`, `pgbench_tellers`,
   `pgbench_history`) and populates them with data. Scale factor controls volume:
   scale 1 = 100,000 accounts (~16 MB), scale 10 = 1,000,000 accounts.

2. **Run workload** (`pgbench -c -T`) — pgbench hammers the database with bank
   transactions for the configured duration and prints a summary to stdout:
   TPS, average latency, latency stddev.

3. **Capture and save** — your Go code parses that stdout into a `pgbench.Result`
   struct and passes it to `state.SaveBenchmarkResult`. The pgbench package never
   touches the state store directly — the test wires them together.

**Two databases are always in play:**
- **target DB** (`dbtest-postgres`) — where pgbench creates tables and runs transactions
- **state DB** (`dbtest-state`) — where your framework saves results for later comparison

pgbench never touches the state DB. Only your Go code does.

**No new infrastructure needed** beyond having pgbench installed locally:
```bash
brew install postgresql        # macOS — pgbench ships with postgresql
sudo apt install postgresql-client  # Ubuntu
```

---

## New concepts introduced in Stage 4

**subprocess** — a child process your Go program starts and manages via `os/exec`.
You build a command, run it, and capture its stdout/stderr as bytes. This is how
Go shells out to external tools like pgbench.

**result parsing** — pgbench prints human-readable text to stdout. The `regexp`
package extracts numbers from that text using pattern matching.

**ports and adapters** — `pgbench` is a pure computation: run a tool, return
structured data. `state` is an adapter: take that data, persist it. Neither
package knows about the other's internals. The test is the glue that connects them.

**UUID** — universally unique identifier generated per row in the state DB.
Better than auto-incrementing integers for distributed systems because a UUID
from your local state DB remains meaningful when you later run against RDS or
Aurora in different environments.

**RunID** — the UUID of the parent row in the `runs` table. Every benchmark
result, validation result, and provider event in Stage 7+ shares a RunID,
which is the thread that ties everything from one test execution together.
One run can produce many benchmark result rows — for example, collecting
metrics every 5 minutes during a 30-minute Stage 7 soak test.

---

## Files to create

```
pgbench/
├── result.go       ← Result struct, CompareResult struct, Compare, Print
├── runner.go       ← Config, Initialize, RunLocal, parsePgbenchOutput (unexported)
└── runner_test.go  ← TestPgbenchLocal
```

## Files to modify

```
telemetry/telemetry.go  ← add BenchmarkTPS (GaugeVec) and BenchmarkLatencyMs (HistogramVec)
state/state.go          ← add benchmark_results table migration, SaveBenchmarkResult,
                           GetLastBenchmarkResult
```

---

## Task 1 — `telemetry/telemetry.go`

Add three new exported fields to the `Telemetry` struct and register them in `Init`.

All three are GaugeVec — pgbench produces one summary number per run, not a
stream of individual observations. A Gauge holds the latest reading and updates
each time pgbench runs. A Histogram would require thousands of individual
per-transaction measurements to be meaningful, which pgbench does not provide
by default.

`BenchmarkTPS *prometheus.GaugeVec` — metric name `dbtest_benchmark_tps`, label `provider`.
Current transactions per second from the most recent pgbench run.

`BenchmarkLatencyAvgMs *prometheus.GaugeVec` — metric name `dbtest_benchmark_latency_avg_ms`,
label `provider`. Average transaction latency in milliseconds from the most recent run.

`BenchmarkLatencyStddevMs *prometheus.GaugeVec` — metric name `dbtest_benchmark_latency_stddev_ms`,
label `provider`. Standard deviation of transaction latency in milliseconds. A low
value means consistent latency; a high value means some transactions were much
slower than others, often indicating lock contention or resource spikes.

---

## Task 2 — `state/state.go`

Add a new `benchmark_results` table to the auto-migration and two new
package-level functions. Follow the same pattern as existing state functions:
pool as first argument, tel as last argument.

**New table schema:**

| column | type | notes |
|---|---|---|
| id | UUID | generated via `gen_random_uuid()`, primary key |
| run_id | UUID | FK to `runs.id` — which test execution produced this result |
| provider | TEXT | e.g. `"local"`, `"rds"`, `"aurora"` |
| tps | FLOAT8 | |
| latency_avg_ms | FLOAT8 | |
| latency_stddev_ms | FLOAT8 | |
| scale_factor | INT | |
| clients | INT | |
| duration_seconds | FLOAT8 | |
| created_at | TIMESTAMPTZ | default `now()` |

**New functions:**

`SaveBenchmarkResult(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, result pgbench.Result, tel *telemetry.Telemetry) error`
— inserts one row into `benchmark_results`. `runID` comes from `state.StartRun`,
`result` comes from `pgbench.RunLocal`. Returns error on insert failure.
Logs `"saved benchmark result"` with `provider` and `tps` if tel is not nil.

`GetLastBenchmarkResult(ctx context.Context, pool *pgxpool.Pool, provider string, tel *telemetry.Telemetry) (*pgbench.Result, error)`
— returns the most recent result for the given provider ordered by `created_at DESC`.
Returns `nil, nil` (not an error) when no previous result exists — caller checks
for nil before comparing. Logs `"loaded previous benchmark result"` with `provider`
and `tps` if tel is not nil and a result was found.

---

## Task 3 — `pgbench/result.go`

**`Result` struct** — plain data, no database or state store knowledge:

| field | type | notes |
|---|---|---|
| TPS | float64 | transactions per second reported by pgbench |
| LatencyAvgMs | float64 | average transaction latency in milliseconds |
| LatencyStddevMs | float64 | latency standard deviation in milliseconds |
| ScaleFactor | int | `-s` flag used during initialize |
| Clients | int | `-c` flag used during run |
| Duration | time.Duration | `-T` flag used during run |
| Provider | string | label only — does not affect pgbench behavior |

**`CompareResult` struct:**

| field | type | notes |
|---|---|---|
| A | Result | baseline result |
| B | Result | comparison result |
| TPSDeltaPct | float64 | positive = B is faster |
| LatDeltaPct | float64 | positive = B has lower latency (better) |

**`Compare(a, b Result) CompareResult`**
— computes relative TPS and latency deltas between two results.

**`(cr CompareResult) Print(w io.Writer)`**
— writes a human-readable summary to w showing TPS and latency for A and B,
the delta percentages, and which provider is faster.

---

## Task 4 — `pgbench/runner.go`

**`Config` struct:**

| field | type | notes |
|---|---|---|
| ScaleFactor | int | scale 1 ≈ 16 MB of data |
| Clients | int | concurrent database connections |
| Duration | time.Duration | how long to run the workload |
| Provider | string | label for metrics and logs only |

**`Initialize(ctx context.Context, dsn string, cfg Config) error`**
— runs `pgbench -i -s <ScaleFactor> <dsn>` as a subprocess. Creates pgbench
tables in the target database. Safe to call again — pgbench drops and recreates.
Returns error with pgbench stdout included in the message on failure.
Does not take tel — initialization has no metrics.

**`RunLocal(ctx context.Context, dsn string, cfg Config, tel *telemetry.Telemetry) (Result, error)`**
— calls `Initialize`, then runs pgbench with flags `-c <Clients> -T <seconds>
-P 5 --no-vacuum`. Captures combined stdout/stderr, calls `parsePgbenchOutput`,
emits metrics and logs `"benchmark complete"` with `provider`, `tps`,
`latency_avg_ms`, `clients`, `duration` if tel is not nil. Returns the Result.

Flags explained:
- `-c` — number of concurrent clients (connections)
- `-T` — duration in seconds
- `-P 5` — print progress every 5 seconds (informational, does not affect results)
- `--no-vacuum` — skip the VACUUM step before running (faster for testing)

**`parsePgbenchOutput(output string, cfg Config) (Result, error)` (unexported)**
— extracts TPS and latency from pgbench stdout using regexp. Must handle two TPS
line variants that exist across Postgres versions:
- `tps = N (without initial connection time)`
- `tps = N (excluding connections establishing)`

Returns error if TPS line is not found. `LatencyStddevMs` is optional — return
zero if absent.

---

## Task 5 — `pgbench/runner_test.go`

**`TestPgbenchLocal(t *testing.T)`**

Skips automatically if `DSN` is not set or pgbench is not installed (`exec.LookPath`).

**How Stage 3's state package works** (read before implementing):
`state.Connect` returns a plain `*pgxpool.Pool`. All state functions are
package-level and take the pool as their first argument. `STATE_DSN` is optional —
if not set, `statePool` stays nil and all state steps are skipped with a nil-guard.
The test still runs and asserts benchmark correctness without the state store.

**Flow:**

1. Init telemetry on port 9091; defer `tel.Shutdown()`
2. If `STATE_DSN` is set, call `state.Connect(ctx, STATE_DSN, tel)` → `statePool`;
   defer `statePool.Close()`. If not set, `statePool` stays nil.
3. If `statePool != nil`, call `state.StartRun(ctx, statePool, state.RunConfig{Seed: 0, Scenario: "pgbench-local"}, tel)`;
   defer `run.End()` immediately
4. Call `pgbench.RunLocal(ctx, DSN, pgbench.Config{ScaleFactor: 1, Clients: 2, Duration: 5 * time.Second, Provider: "local"}, tel)`
5. Assert `result.TPS > 0` and `result.LatencyAvgMs > 0`
6. If `statePool != nil`, call `state.SaveBenchmarkResult(ctx, statePool, run.ID, result, tel)`
7. If `statePool != nil`, call `state.GetLastBenchmarkResult(ctx, statePool, "local", tel)` —
   if previous result exists, call `pgbench.Compare` and `Print(os.Stdout)`;
   log a warning (not `t.Fatal`) if TPS dropped more than 20%

---

## Run all tests

```bash
docker start dbtest-postgres dbtest-state

DSN="postgres://postgres:test@localhost/postgres" \
STATE_DSN="postgres://postgres:test@localhost:5433/postgres" \
go test ./... -v
```

The pgbench test skips automatically if pgbench is not installed.
All Stage 1–3 tests must still pass unchanged.

---

## Expected log output (new in Stage 4)

```
{"level":"INFO","msg":"pgbench initialized","scale_factor":1,"provider":"local"}
{"level":"INFO","msg":"benchmark complete","provider":"local","tps":842.3,"latency_avg_ms":4.75,"clients":2,"duration":"5s"}
{"level":"INFO","msg":"saved benchmark result","provider":"local","tps":842.3}
```

New lines at `curl localhost:9091/metrics | grep dbtest_benchmark`:
```
dbtest_benchmark_tps{provider="local"} 842.3
dbtest_benchmark_latency_ms_bucket{provider="local",le="5"} 1
```

---

## Package dependency rules

```
telemetry/  ← stdlib + prometheus client only (unchanged)
adapter/    ← imports telemetry
benchmark/  ← imports adapter, pkg/seedgen, telemetry (unchanged)
validator/  ← imports adapter, telemetry (unchanged)
pgbench/    ← imports telemetry only — no state, no adapter, no benchmark
state/      ← imports pgbench (for pgbench.Result in SaveBenchmarkResult)
```

`pgbench` does not import `state`. `state` imports `pgbench` for the `Result`
type. The test imports both and wires them together. Circular imports are not
allowed in Go — the dependency must always be one-directional.

When HammerDB arrives in a later stage, it follows the same pattern:
`hammerdb.Result` is a plain struct, `state` imports `hammerdb` for that type,
the test wires them together.

---

## Notes for the agent

- Add comments explaining what each regexp pattern matches — the author is a beginner
- `tel` is always optional — nil-check before every use
- `parsePgbenchOutput` is unexported — implementation detail, not public API
- The regression warning in step 7 is `t.Logf` not `t.Fatal` — hard gates belong in a later stage
- Do not use the `-j` (jobs) flag — it complicates output parsing unnecessarily
- Use `uuid.UUID` type for RunID, not `int64` — the schema uses `gen_random_uuid()`
- `pgbench.Result` has no ID field — IDs belong to the state DB, not to the result