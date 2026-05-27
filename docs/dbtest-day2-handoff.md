# dbtest — Day 2 Handoff

## Project context

`dbtest` is a Go framework for testing PostgreSQL databases in a reproducible way.
Module: `github.com/elenaochkina/dbtest`

**Core concept:** seed a database with deterministic data → run an operation → verify
data consistency via checksum. Same seed always produces the same data, so the
checksum baseline is always predictable.

---

## What was completed in Day 1

- `adapter/adapter.go` — `Connect(dsn string) (*pgxpool.Pool, error)` opens a pgx
  connection pool and pings Postgres before returning
- `adapter/adapter_test.go` — integration test: connect → create temp table → insert
  3 rows → assert COUNT(*) == 3
- `pkg/seedgen/seedgen.go` — empty stub, ready to implement
- Local Postgres running in Docker:
  - Container: `dbtest-postgres`
  - DSN: `postgres://postgres:test@localhost/postgres`
  - Start: `docker start dbtest-postgres`

Test passes:
```
DSN="postgres://postgres:test@localhost/postgres" go test ./adapter/... -v
```

---

## Day 2 goal

Complete the **seed → checksum loop**:

```
seedgen(42) → INSERT warehouse rows → checksum before
            → runOrderCycle()       → checksum after
            → assert delta is exactly what we expect
```

---

## Files to create

```
pkg/seedgen/seedgen.go          ← deterministic RNG wrapper
benchmark/seed.go               ← INSERT warehouse rows using seedgen
validator/validator.go          ← compute COUNT + SUM checksum
benchmark/warehouse_test.go     ← integration test tying it all together
```

---

## Task 1 — `pkg/seedgen/seedgen.go`

A thin wrapper around Go's built-in RNG with a fixed seed.
Same seed = same sequence of numbers every time.

**Requirements:**
- Use `math/rand/v2`
- `New(seed int64) *Seeder` — creates a seeded RNG
- `Seeder.Intn(n int) int` — returns next int in sequence
- `Seeder.StockCount() int` — returns a stock value between 1000-9999

**Important:** `StockCount()` does not return the same number every call.
It returns the *next number in the sequence*. Think of it like drawing from
a pre-shuffled deck — same seed = same deck order every run.

```go
package seedgen

import "math/rand/v2"

type Seeder struct {
    rng *rand.Rand
}

func New(seed int64) *Seeder {
    return &Seeder{
        rng: rand.New(rand.NewPCG(uint64(seed), 0)),
    }
}

func (s *Seeder) Intn(n int) int {
    return s.rng.Intn(n)
}

func (s *Seeder) StockCount() int {
    return s.Intn(9000) + 1000
}
```

---

## Task 2 — `benchmark/seed.go`

Seeds a `warehouse` table with deterministic rows using seedgen.

**Requirements:**
- `CreateWarehouseTable(ctx, db)` — creates the table if not exists
- `SeedWarehouses(ctx, db, seeder, count int)` — inserts `count` warehouse rows
- `DropWarehouseTable(ctx, db)` — drops the table (for test cleanup)

**Warehouse table schema:**
```sql
CREATE TABLE IF NOT EXISTS warehouse (
    id          SERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    stock_count INT  NOT NULL
)
```

**Each row:**
- `name` = `fmt.Sprintf("Warehouse-%d", i)`
- `stock_count` = `seeder.StockCount()` ← advances RNG by one position per row

---

## Task 3 — `validator/validator.go`

Computes a checksum of the warehouse table.

**Requirements:**
- `Checksum` struct with `RowCount int64` and `StockSum int64`
- `ComputeChecksum(ctx, db, table string) (Checksum, error)` — runs:

```sql
SELECT COUNT(*), COALESCE(SUM(stock_count), 0) FROM warehouse
```

- `AssertDelta(t, before, after Checksum, expectedStockDelta int64)` — asserts:
  - `RowCount` did not change
  - `StockSum` changed by exactly `expectedStockDelta`

---

## Task 4 — `benchmark/warehouse_test.go`

Integration test tying everything together.

**Requirements:**
- Skip if `DSN` env var is not set
- Drop and recreate the warehouse table at the start (clean slate)
- Seed 5 warehouses with `seedgen.New(42)`
- Capture `checksum_before`
- Run `runOrderCycle()` — see spec below
- Capture `checksum_after`
- Assert delta: `StockSum` decreased by exactly 10, `RowCount` unchanged

**`runOrderCycle()` spec:**
- Pick warehouse id=1 (hardcode for now, no randomness needed yet)
- Decrease its `stock_count` by 10
- Insert one row into an `orders` table: `(warehouse_id, quantity)`

**Orders table schema:**
```sql
CREATE TABLE IF NOT EXISTS orders (
    id           SERIAL PRIMARY KEY,
    warehouse_id INT NOT NULL,
    quantity     INT NOT NULL
)
```

**Expected checksum delta:**
```
before: { RowCount: 5, StockSum: <deterministic value from seed 42> }
after:  { RowCount: 5, StockSum: before.StockSum - 10 }
```

---

## Run the test

```bash
DSN="postgres://postgres:test@localhost/postgres" go test ./... -v
```

All tests should pass including the existing `adapter` test.

---

## Package dependency rules

```
pkg/seedgen/   ← no imports from this project (pure utility)
benchmark/     ← imports adapter, pkg/seedgen
validator/     ← imports adapter
```

`seedgen` must NOT import `benchmark` or `validator`.
`validator` must NOT import `benchmark`.
Keep dependencies one-directional.

---

## Notes for the agent

- The author is a beginner — add comments explaining what each function does
- Use `context.Background()` in tests for simplicity
- Use `pgx/v5` and `pgxpool` consistently (already used in adapter)
- If a step is unclear, implement the simplest version that makes the test pass
- Do not add Prometheus metrics, logging, or any Stage 2+ features yet
