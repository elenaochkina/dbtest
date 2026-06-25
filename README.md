# dbtest

A Go framework for testing PostgreSQL databases in a reproducible way.

Seed a database with deterministic data → run an operation → verify data consistency via checksum.
The same seed always produces the same rows, so the checksum baseline is always predictable.

## Phase 1 — seed → checksum loop

Packages built in Phase 1:

| Package | What it does |
|---|---|
| `adapter` | Opens a `pgxpool` connection and pings Postgres |
| `pkg/seedgen` | Deterministic RNG wrapper — same seed, same sequence every run |
| `benchmark` | Creates and seeds a `warehouse` table |
| `validator` | Computes `COUNT(*) + SUM(stock_count)` checksum and asserts deltas |

## Prerequisites

```bash
# Start Postgres (create once, then start on subsequent runs)
docker run -d \
  --name dbtest-postgres \
  -e POSTGRES_PASSWORD=test \
  -p 5432:5432 \
  postgres:16

# Already created? Just start it:
docker start dbtest-postgres
```

## Run Phase 1 tests

```bash
DSN="postgres://postgres:test@localhost/postgres" go test ./... -v -count=1
```

Expected output:

```
--- PASS: TestConnect (0.03s)
--- PASS: TestWarehouseChecksum (0.03s)
```

## Git hooks

This repo ships a pre-commit hook (`hooks/pre-commit`) that gofmt-formats staged
Go files and verifies the build before each commit. It is version-controlled but
not active by default — activate it once per clone:

```bash
git config core.hooksPath hooks
```
