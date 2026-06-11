# dbtest — Scenario Package Design

## Concept

A **scenario** owns both the control plane and the data plane:

- **Control plane** — provisions a database cluster, waits for it to be ready, deprovisions it on exit
- **Data plane** — runs a workload (warehouse consistency check, pgbench stress test, or both) against the cluster

The caller (`main.go`) only calls `scenario.New()` and `s.Run()`. It never touches providers, clusters, or workloads directly.

---

## Package structure

```
scenario/
├── scenario.go  ← Scenario interface + ScenarioName + Config + registry + New()
└── base.go      ← baseScenario — implements the full lifecycle
```

---

## Flowchart

```
main.go
  │
  ├── parse flags (-provider, -scenario, -seed, -warehouses, -scale, -clients, -duration)
  ├── init telemetry
  ├── connect state DB (optional)
  │
  └── scenario.New("warehouse", Config{Provider: "docker", StatePool, ...})
        │
        └── scenario.Run(ctx, tel)
              │
              ▼
        ┌─────────────────────────────────┐
        │         CONTROL PLANE           │
        │                                 │
        │  provider.Run("docker", tel)    │
        │         ↓                       │
        │  p.Provision(ctx)               │
        │         ↓                       │
        │  state.RecordCluster(...)       │  ← if StatePool != nil
        │         ↓                       │
        │  defer Deprovision + Mark       │  ← guaranteed cleanup
        │         ↓                       │
        │  p.WaitForReady(ctx, cluster)   │
        └─────────────┬───────────────────┘
                      │  cluster.DSN
                      ▼
        ┌─────────────────────────────────┐
        │           DATA PLANE            │
        │                                 │
        │  workload.Run(ctx, DSN, tel)    │
        │                                 │
        │  warehouse:                     │
        │    drop/create tables           │
        │    seed rows                    │
        │    checksum → order → checksum  │
        │    assert delta == -10          │
        │                                 │
        │  pgbench:                       │
        │    pgbench.RunLocal(...)        │
        │    log TPS + latency            │
        │                                 │
        │  all:                           │
        │    Sequential(warehouse, pgbench│
        └─────────────────────────────────┘
              │
              ▼
        ┌─────────────────────────────────┐
        │         CLEANUP (defer)         │
        │  p.Deprovision(ctx, cluster.ID) │
        │  state.MarkDeprovisioned(...)   │
        └─────────────────────────────────┘
```

---

## Interfaces

```go
// Scenario owns the full lifecycle — control plane + data plane.
type Scenario interface {
    Name() string
    Run(ctx context.Context, tel *telemetry.Telemetry) error  // no DSN — provisions its own cluster
}

// Config combines control plane and data plane parameters.
type Config struct {
    Provider  provider.ProviderName
    StatePool *pgxpool.Pool // optional — orphan tracking skipped when nil
    // warehouse
    Seed       int64
    Warehouses int
    // pgbench
    ScaleFactor int
    Clients     int
    Duration    time.Duration
}
```

---

## Registration pattern

Each scenario self-registers via `init()`. Adding a new scenario requires only a new file — `scenario.go` never changes.

```go
// warehouse.go (future)
func init() {
    Register(Warehouse, func(cfg Config) (Scenario, error) {
        w, err := workload.New(workload.Warehouse, toWorkloadConfig(cfg))
        if err != nil {
            return nil, err
        }
        return &baseScenario{name: "warehouse", cfg: cfg, w: w}, nil
    })
}
```

---

## Key design decisions

| Decision | Reason |
|---|---|
| `Run(ctx, tel)` — no DSN | scenario owns provisioning; caller never sees the cluster |
| `StatePool` in Config | optional — works without state DB, loses orphan tracking |
| `baseScenario` handles lifecycle | all scenarios share the same provision/deprovision logic |
| `workload.New()` called inside constructor | error surfaces at `scenario.New()` call time |
| Caller blank-imports `provider/docker` | scenario doesn't force a specific provider implementation |

---

## CLI usage

```bash
# run warehouse consistency check with docker
go run ./cmd/runbenchmark/ -scenario warehouse -provider docker

# run pgbench with custom params
go run ./cmd/runbenchmark/ -scenario pgbench -provider docker -clients 8 -duration 30s

# run both in sequence (default)
go run ./cmd/runbenchmark/ -scenario all

# warehouse with custom seed and row count
go run ./cmd/runbenchmark/ -scenario warehouse -seed 123 -warehouses 10
```

---

## External resource metadata

Every provisioned cluster is an external resource that exists independently of the
process that created it. If the process crashes, the cluster keeps running and costs
money (RDS) or wastes ports (Docker). The `clusters` table is the source of truth
for all external resources so they can always be found and cleaned up.

### clusters table (extended)

```sql
CREATE TABLE clusters (
    id               TEXT        PRIMARY KEY,   -- provider ID (container ID, RDS instance ID)
    provider         TEXT        NOT NULL,       -- "docker", "aws"
    dsn              TEXT        NOT NULL,       -- connection string for reconnect
    scenario         TEXT        NOT NULL,       -- which scenario provisioned this cluster
    status           TEXT        NOT NULL,       -- "running" | "deprovisioned"
    provisioned_at   TIMESTAMPTZ NOT NULL,
    deprovisioned_at TIMESTAMPTZ,                -- null until deprovisioned
    heartbeat_at     TIMESTAMPTZ                 -- updated every 30s while in use
);
```

Two fields are added beyond the original design:

- `scenario` — which scenario created the cluster. Used by the pre-provision check
  to find stale clusters for the same scenario before creating a new one.
- `heartbeat_at` — updated periodically while the cluster is in use. A cluster
  whose heartbeat stopped more than N minutes ago is considered orphaned even if
  its status is still `running`.

---

### Flow 1 — Pre-provision orphan check

Before provisioning a new cluster, scan for any `status='running'` clusters for
the same scenario and provider. If found, attempt to deprovision them first.

```
scenario.Run(ctx, tel)
  │
  ├── state.FindRunningClusters(ctx, pool, scenario, provider)
  │     ↓
  │   for each stale cluster:
  │     p.Deprovision(ctx, cluster.ID)
  │     state.MarkDeprovisioned(ctx, pool, cluster.ID)
  │
  └── p.Provision(ctx)  ← safe to provision now
```

This prevents accumulating orphaned clusters when a process is repeatedly
restarted (e.g. during development or CI retries).

---

### Flow 2 — Heartbeat while running

After provisioning, a background goroutine updates `heartbeat_at` every 30 seconds
until the scenario finishes. If the process crashes, the heartbeat stops. The cleanup
job uses this to detect orphans:

```
clusters WHERE status = 'running'
         AND heartbeat_at < now() - interval '5 minutes'
```

These clusters have a live `status` but a dead process — they are orphans.

---

### Flow 3 — Reconnect after failure

If a process restarts and finds a `status='running'` cluster with a **recent**
heartbeat (within the last 2 minutes), it can reconnect to the existing cluster
instead of provisioning a new one:

```
state.FindRunningClusters(ctx, pool, scenario, provider)
  │
  ├── heartbeat_at > now() - 2min  →  reconnect via cluster.DSN
  │                                    skip Provision + WaitForReady
  │
  └── heartbeat_at older           →  deprovision + provision fresh
```

This covers the pgbench crash case: if pgbench fails halfway, the container is
still running and healthy. On restart, the scenario reconnects to it, skips
provisioning, and retries the workload immediately.

---

### Summary of guarantees

| Situation | What happens |
|---|---|
| Normal exit | defer runs → Deprovision → MarkDeprovisioned → heartbeat stops |
| Process crash | heartbeat stops → cleanup job finds orphan → deprovisions |
| Process restart (recent heartbeat) | reconnects to existing cluster → no new container |
| Process restart (stale heartbeat) | pre-provision check deprovisions old → provisions fresh |
| Deprovision fails (retried 3x) | status stays `running` → cleanup job handles it later |

---

## Package dependencies

```
scenario/   ← imports provider/, workload/, state/, telemetry/
workload/   ← imports benchmark/, pgadapter/, pgbench/, validator/, telemetry/
provider/   ← imports telemetry/ only
state/      ← imports provider/, telemetry/
```

No cycles. `scenario` sits at the top of the stack — only `main.go` and tests import it.
