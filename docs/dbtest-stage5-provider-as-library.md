# dbtest — Stage 5: Provider as Library

## Project context

`dbtest` is a Go framework for testing PostgreSQL databases in a reproducible way.
Module: `github.com/elenaochkina/dbtest`

---

## What changes in Stage 5

Until now, a test needs a Postgres that **already exists**. You had to run
`docker start dbtest-postgres` before running any test. If you forgot, the test
failed immediately. The test had no control over the database lifecycle.

Stage 5 gives the test that control. The test itself says "give me a Postgres" —
it does not care how that happens, it just needs a DSN back. It runs its logic,
then says "destroy it." The database lifetime is tied to the test lifetime, not
to something a human had to set up beforehand.

---

## New concepts introduced in Stage 5

### Provider interface

A Go interface defines a contract — a set of method signatures. Any struct that
implements all of those methods satisfies the interface. The test calls
`p.Provision(ctx)` without knowing whether `p` is Docker or RDS. The test never
imports `provider/docker` or `provider/aws` directly.

### Factory function

`factory.Run(providerName string) (Provider, error)` is the factory. It takes a
provider name, switches on it, and returns the right implementation as a `Provider`
interface. The caller never sees the concrete struct underneath.

The factory lives in its own package `provider/factory/` to avoid a circular import.
If factory lived in the same package as the interface (`provider/`), you would get:
`provider` imports `provider/docker` which imports `provider` — the compiler rejects this.
Putting factory in its own package breaks the cycle cleanly.

### Env vars inside constructors

Provider-specific config is read from environment variables **inside each provider's
constructor**. Nothing is passed through the factory. Nothing comes from a config file.

This is the right pattern for Kubernetes. In k8s you inject env vars via Pod spec
or ConfigMap. Each provider reads what it needs at startup. No files to mount, no
flags to thread through layers.

Examples:
- `docker.New(tel)` reads `DOCKER_PG_IMAGE` — defaults to `"postgres:16"` if not set
- `aws.New(tel)` reads `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`

### Concrete provider structs

Each provider is its own unexported struct in its own package. Unexported means
lowercase — `dockerProvider`, not `DockerProvider`. The outside world never sees
the struct, only the `Provider` interface. This enforces that callers always go
through the factory.

Each struct holds the SDK client specific to that provider:
- `dockerProvider` holds a `*dockerclient.Client` (import alias for `github.com/docker/docker/client`)
- `awsProvider` holds a `*rds.Client` from the AWS SDK plus a `region string`

The Docker client field is named `client`, not `cli`, to avoid confusion with
"command line interface." The import is aliased as `dockerclient` to avoid a
naming conflict between the field name and the package name.

Go checks interface satisfaction automatically at compile time — if a method is
missing or has the wrong signature, the compiler tells you immediately.

### Telemetry passed directly to constructors

Both `docker.New(tel *telemetry.Telemetry)` and `aws.New(tel *telemetry.Telemetry)`
accept telemetry as a plain parameter — not via functional options. Pass `nil` if
telemetry is not needed; every usage site checks `if tel != nil` before emitting.

This is simpler and more readable than the functional options pattern, which adds
complexity only justified when there are many optional parameters in a public SDK.

### ClusterInfo

`ClusterInfo` is what comes back after provisioning — the DSN to connect with and
the ID to use when deprovisioning. This is all the test needs to know about the cluster.

### clusters table in the state store

Every provisioned cluster is recorded in the state DB immediately after `Provision`
returns. This solves the orphan problem: if the process crashes after provisioning
but before `defer Deprovision` fires, the cluster record survives in the state DB.
A future cleanup job can scan for clusters with `status='running'` older than a TTL
and deprovision them.

The `clusters` table has these columns:
- `id` — the provider-specific cluster identifier (container ID, RDS instance ID)
- `provider` — which provider created it (`"docker"`, `"aws"`)
- `dsn` — the connection string
- `status` — `"running"` or `"deprovisioned"`
- `provisioned_at` — timestamp when the cluster was created
- `deprovisioned_at` — timestamp when it was destroyed (nullable)

The state write lives in `main.go` and the workflow caller, not inside the provider.
This keeps `provider/docker` and `provider/aws` stateless — they do not depend on
the state DB being available. If the state DB is down, provisioning still works;
you just lose the orphan-tracking record for that run.

In Stage 7 when Temporal arrives, `workflow_id` and `run_id` columns will be added
to this table via auto-migration so orphan recovery can cross-reference Temporal's
execution state.

### `defer p.Deprovision(...)` — guaranteed cleanup

The `defer` keyword schedules a function to run when the surrounding function
returns — whether that is a normal return, a test failure, or a panic. This prevents
orphaned containers or RDS instances when a test crashes halfway through.

Always use `context.Background()` in the deferred call, not the test's context.
The test context may already be cancelled by the time defer fires — using it would
cause Deprovision to fail silently and leave the cluster running.

### `main.go` uses `-provider` flag, no DSN fallback

`main.go` always provisions via `factory.Run`. The `DSN` env var fallback is removed.
One code path, always exercising the full lifecycle. Provider name comes from a
`-provider` CLI flag with default `"docker"`.

### Provider metrics

Two new Prometheus metrics, both with a `provider` label:

- `dbtest_provider_provision_duration_seconds{provider="docker"}`
- `dbtest_provider_deprovision_total{provider="docker"}`

When you add RDS the same metrics appear with `provider="aws"`. Grafana can compare
them side by side without any dashboard changes.

---

## Repository layout after Stage 5

New files to create:

```
provider/
├── provider.go            ← Provider interface + ClusterInfo
├── docker/
│   ├── docker.go          ← dockerProvider struct + Provision + WaitForReady + Deprovision
│   └── docker_test.go     ← integration test for full Docker lifecycle
├── aws/
│   └── aws.go             ← awsProvider struct + Provision + WaitForReady + Deprovision
└── factory/
    └── factory.go         ← Run(providerName string) (provider.Provider, error)
```

Files to modify:

- `telemetry/telemetry.go` — add `ProviderProvisionDuration` and `ProviderDeprovisionTotal`
- `state/state.go` — add `clusters` table + `RecordCluster` and `MarkDeprovisioned` methods
- `cmd/runbenchmark/main.go` — read `-provider` flag, call `factory.Run`, remove DSN fallback, record cluster to state DB

---

## Task 1 — `provider/provider.go`

Define the interface and data types only. No factory logic here.

### ClusterInfo

Define a `ClusterInfo` struct with two fields:
- `ID string` — the provider-specific identifier used to deprovision the cluster
- `DSN string` — the connection string the test uses to connect

### Provider interface

Define a `Provider` interface with three methods:

- `Provision(ctx context.Context) (ClusterInfo, error)` — create a new Postgres instance
- `WaitForReady(ctx context.Context, cluster ClusterInfo) error` — poll until the database accepts connections
- `Deprovision(ctx context.Context, clusterID string) error` — destroy the instance and free all resources

No `ProvisionRequest` — this framework is Postgres-only. Provider-specific config
goes into each constructor via env vars, not into `Provision`.

---

## Task 2 — `provider/factory/factory.go`

The factory. One function, one responsibility.

Define `Run(providerName string, tel *telemetry.Telemetry) (provider.Provider, error)`.
Inside, use a switch on `providerName`:

- `"docker"` — call `docker.New(tel)` and return the result
- `"aws"` — call `aws.New(tel)` and return the result
- default — return an error saying the provider name is unknown

`factory.go` is the only file in the entire codebase that imports `provider/docker`
and `provider/aws`. Everything else imports only `provider` or `provider/factory`.

---

## Task 3 — `provider/docker/docker.go`

Add these dependencies:

- `github.com/docker/docker/client` — import aliased as `dockerclient`
- `github.com/docker/docker/api/types/container`
- `github.com/docker/go-connections/nat`

### Struct

Define an unexported `dockerProvider` struct with three fields:
- `client *dockerclient.Client` — the Docker SDK client (named `client`, not `cli`)
- `image string` — the Postgres image to use
- `tel *telemetry.Telemetry` — nil if telemetry was not provided

### Constructor: `New(tel *telemetry.Telemetry) (*dockerProvider, error)`

This is what the factory calls. It:
- Reads `DOCKER_PG_IMAGE` env var; defaults to `"postgres:16"` if not set
- Connects to the local Docker daemon using `dockerclient.FromEnv`
- Returns the struct with `tel` stored directly as a field

No functional options. Telemetry is a plain parameter. Pass `nil` if not needed.

### Provision

Steps in order:

1. Call `ContainerCreate` with `POSTGRES_PASSWORD=test` and `POSTGRES_DB=postgres`
   as env vars. Set `PublishAllPorts: true` so Docker assigns a random free host
   port — this avoids conflicts when multiple containers run at the same time.
2. Call `ContainerStart`.
3. Call `ContainerInspect` to find which host port was assigned to container port 5432.
4. Build the DSN: `postgres://postgres:test@localhost:<hostport>/postgres`.
5. If `tel` is not nil, observe the duration into `ProviderProvisionDuration` with
   label `"docker"` and emit a structured log line with container ID and host port.
6. Return `ClusterInfo` with the container ID as `ID` and the DSN.

### WaitForReady

Poll every 500ms for up to 30 seconds. Each iteration try to open a pgx connection
to the DSN — if it succeeds, ping it, close it, and return nil. If the deadline
passes, return an error. Log a line when the cluster becomes ready.

### Deprovision

1. Call `ContainerStop` with a 5-second grace period.
2. Call `ContainerRemove` with `RemoveVolumes: true` and `Force: true`.
3. If `tel` is not nil, increment `ProviderDeprovisionTotal` with label `"docker"`
   and emit a structured log line.

---

## Task 4 — `provider/aws/aws.go`

Add this dependency:

- `github.com/aws/aws-sdk-go-v2/service/rds`

### Struct

Define an unexported `awsProvider` struct with three fields:
- `client *rds.Client` — the AWS RDS SDK client
- `region string` — the AWS region
- `tel *telemetry.Telemetry` — nil if telemetry was not provided

### Constructor: `New(tel *telemetry.Telemetry) (*awsProvider, error)`

This is what the factory calls. It:
- Reads `AWS_REGION` env var; returns an error if not set
- Reads credentials from the default AWS credential chain
- Constructs the RDS client
- Returns the struct with `tel` stored directly as a field

No functional options. Same pattern as the Docker constructor.

### Provision

Call the RDS `CreateDBInstance` API. Return the instance endpoint as the DSN once
known. If `tel` is not nil, emit the provision duration metric with label `"aws"`.

### WaitForReady

Poll `DescribeDBInstances` until the instance status is `"available"`. RDS instances
take minutes, so use a 15-minute timeout rather than 30 seconds.

### Deprovision

Call `DeleteDBInstance` with `SkipFinalSnapshot: true`. If `tel` is not nil, emit
the deprovision counter metric with label `"aws"`.

---

## Task 5 — add `clusters` table to `state/state.go`

Add auto-migration for a `clusters` table with these columns:

- `id text primary key` — provider-specific cluster identifier
- `provider text not null` — `"docker"` or `"aws"`
- `dsn text not null` — connection string
- `status text not null` — `"running"` or `"deprovisioned"`
- `provisioned_at timestamptz not null`
- `deprovisioned_at timestamptz` — nullable, set when deprovisioned

Add two methods to the state client:

- `RecordCluster(ctx, ClusterInfo, providerName string) error` — inserts a row with `status='running'`
- `MarkDeprovisioned(ctx, clusterID string) error` — updates `status='deprovisioned'` and sets `deprovisioned_at`

The state client already auto-migrates on connect — add the `clusters` table to
the existing migration block. No separate migration tool needed.

Note: Temporal columns (`workflow_id`, `run_id`) will be added in Stage 7 via the
same auto-migration mechanism when Temporal is introduced.

---

## Task 6 — add two metrics to `telemetry/telemetry.go`

Add to the `Telemetry` struct:

- `ProviderProvisionDuration *prometheus.HistogramVec` — metric name `dbtest_provider_provision_duration_seconds`, label `provider`
- `ProviderDeprovisionTotal *prometheus.CounterVec` — metric name `dbtest_provider_deprovision_total`, label `provider`

Use wider histogram buckets than the query-latency ones because provisioning takes
seconds not milliseconds: `{0.5, 1, 2, 5, 10, 30, 60}`.

Register both in `Init` using the custom registry, same pattern as existing metrics.

---

## Task 7 — `provider/docker/docker_test.go`

Test the full Docker lifecycle: provision → wait → connect → deprovision.

Skip unless `DOCKER_TEST=1` is set. Use a 2-minute context timeout. Steps:

1. Init telemetry on port 9091.
2. Call `factory.Run("docker", tel)` — always go through the factory.
3. Call `Provision(ctx)`.
4. Immediately defer `Deprovision(context.Background(), cluster.ID)`.
5. Call `WaitForReady(ctx, cluster)`.
6. Open a pgx connection and run `SELECT 1+1`, assert the result is 2.

---

## Task 8 — update `cmd/runbenchmark/main.go`

Remove the `DSN` env var entirely. Replace with:

1. Read `-provider` CLI flag with default `"docker"` using `flag.String` and `flag.Parse`.
2. Call `factory.Run(*providerFlag, tel)` to get a `Provider`.
3. Call `Provision(ctx)`, defer `Deprovision(context.Background(), cluster.ID)`.
4. Call state client `RecordCluster(ctx, cluster, *providerFlag)` immediately after provision.
5. Call `WaitForReady(ctx, cluster)`.
6. Connect via `pgadapter.Connect(cluster.DSN, tel)` — everything from here is identical to Stage 4.
7. After `Deprovision` fires (via defer), call `MarkDeprovisioned(ctx, cluster.ID)`.

Note: step 7 cannot use defer order to guarantee sequencing — `MarkDeprovisioned`
must run after `Deprovision`. Use a named cleanup function rather than two separate
defers to control the order explicitly.

---

## How to run

Run with Docker provider — creates and destroys its own Postgres:

```
DOCKER_TEST=1 go test ./... -v -count=1 -provider=docker
```

Run the dev tool:

```
go run ./cmd/runbenchmark/ -provider=docker
```

---

## Expected metrics output

After a test run, curling `localhost:9090/metrics | grep dbtest_provider` should show:

- `dbtest_provider_provision_duration_seconds_count{provider="docker"} 1`
- `dbtest_provider_provision_duration_seconds_sum{provider="docker"}` — some value in seconds
- `dbtest_provider_deprovision_total{provider="docker"} 1`

---

## Package dependency rules

```
provider/          ← imports nothing from this project (base, everything builds on it)
provider/docker/   ← imports provider/, telemetry/, Docker SDK, pgx
provider/aws/      ← imports provider/, telemetry/, AWS SDK
provider/factory/  ← imports provider/, provider/docker/, provider/aws/, telemetry/
state/             ← imports provider/ (for ClusterInfo in RecordCluster signature)
benchmark/         ← imports provider/, provider/factory/ (never sub-packages directly)
cmd/runbenchmark/  ← imports provider/, provider/factory/, state/, telemetry/, pgadapter/
```

Why this works without circular imports:
- `provider/` is at the bottom — it imports nothing from this project
- `provider/docker/` and `provider/aws/` import `provider/` but not each other
- `provider/factory/` imports all three but nothing imports it except cmd and tests
- `state/` imports `provider/` for `ClusterInfo` only — no cycle forms

---

## Notes for the agent

- Both constructors use `New(tel *telemetry.Telemetry)` — no functional options pattern.
  Pass `nil` for telemetry when not needed; check `if p.tel != nil` before every metric emit.
- The Docker client field is named `client`, not `cli`. Import the Docker client package
  as `dockerclient` to avoid the naming conflict between the field and the package.
- `provider/factory/factory.go` is the only file that imports `provider/docker` and
  `provider/aws`. Everything else imports `provider/factory` only.
- Both `dockerProvider` and `awsProvider` are unexported structs.
- No `ProvisionRequest` — Postgres-only framework.
- Provider name comes from a `-provider` CLI flag. The factory receives the string only.
- Use `context.Background()` in all deferred `Deprovision` calls, not the test context.
- The `clusters` table write happens in the caller (`main.go`), not inside the provider.
  Providers are stateless — they do not depend on the state DB.
- In Stage 7, add `workflow_id text` and `run_id text` columns to `clusters` via
  the existing auto-migration mechanism.
- The AWS provider will not be testable without real credentials. Wrap its test with
  an `AWS_TEST=1` skip guard the same way Docker uses `DOCKER_TEST=1`.
- All Stage 1–4 tests using the `DSN` env var must still pass unchanged.
- Do NOT change the `Checksum` struct or `AssertDelta` signature.



## To DISCUSS
any other ways to deprovision if the first attempt is failed?

⏺ A few options:

  1. Retry loop (what I was about to add) — simple, handles transient Docker daemon errors. Doesn't
  help if the daemon is completely down.
  2. Shell out to docker rm -f <id> as a fallback — bypasses the SDK entirely. Useful if the SDK client
   is in a bad state but the daemon itself is still running.
  3. Leave it to the state DB + cleanup job — accept that Deprovision can fail, keep the clusters row
  as running, and let a separate process (Stage 7 Temporal worker) poll for stale rows and clean them
  up. No retry in-process at all.
  4. Combination: retry once immediately, then fall back to shell-out, then give up and rely on the
  cleanup job.

  For a local Docker provider the most practical is retry + shell fallback — Docker failures are
  usually transient. For RDS, retrying DeleteDBInstance is safe since it's idempotent. Which direction
  do you want?

✻ Sautéed for 7s

❯ explain retrying DeleteDBInstance is safe since it's idempotent?

⏺ If DeleteDBInstance fails mid-call, one of two things happened:

  1. The request never reached AWS — the instance still exists. Retrying calls DeleteDBInstance again
  on a live instance. AWS deletes it. Safe.
  2. The request reached AWS but the response was lost — AWS is already deleting the instance. Retrying
   calls DeleteDBInstance on an instance that's being deleted or is already gone. AWS returns an error
  like DBInstanceNotFound. You catch that specific error, treat it as success, and move on.

  So the retry logic for RDS needs to distinguish:
  - DBInstanceNotFound → already deleted, treat as success
  - Any other error → genuine failure, retry or give up

  That's what makes it safe — you handle the "already gone" case explicitly rather than treating all
  errors the same way.

  Docker ContainerRemove with Force: true behaves the same way — if the container is already gone, the
  SDK returns a "no such container" error, which you catch and treat as success.