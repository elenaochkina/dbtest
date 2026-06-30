# AWS Provider Setup — Options & Trade-offs (focus: RDS)

This doc lays out the decision space for adding an AWS provider to `dbtest`. It is not a
single prescriptive plan — each section presents the realistic options, their trade-offs, and
a recommendation. Decisions made here feed the eventual `provider/aws/` implementation, which
plugs into the existing self-registering `Provider` registry (`provider/provider.go`) exactly
like `provider/docker/docker.go`.

The `Provider` contract the AWS path must satisfy (unchanged):

```go
Provision(ctx, ProvisionRequest) (ClusterInfo, error)   // ProvisionRequest{VCPU, MemoryMiB, DiskGiB}
WaitForReady(ctx, ClusterInfo) error
Deprovision(ctx, clusterID) error
// optional: FailureInjector.KillProcess(ctx, ClusterInfo) (ClusterInfo, error)
```

---

## Decision 1 — What does "AWS provider" actually provision?

| Option | What it is | Pros | Cons |
|---|---|---|---|
| **A. RDS instance** (recommended) | Single-instance managed Postgres via `CreateDBInstance` | Closest match to "a Postgres cluster"; managed; realistic prod target; supports failover later | Minutes to provision; real cost; networking setup |
| B. RDS Aurora cluster | `CreateDBCluster` + writer instance | Real failover, read replicas, prod-grade | More moving parts; slower; pricier; overkill for current scenarios |
| C. Postgres on EC2 | Launch EC2 + install/run Postgres (or a container) | Full control; cheap with spot; SIGKILL-style failure injection like Docker | You own AMIs, bootstrap, patching, teardown — most code |
| D. ECS/Fargate Postgres | Run the `postgres` image as a Fargate task | Fast-ish; container parity with Docker provider | Not a "real" managed DB; ephemeral storage caveats |

**Recommendation: A (single RDS instance).** It matches the `provider.AWS` intent, is the
canonical managed-Postgres target, and keeps the first cut small. Aurora (B) is a clean
follow-up if multi-AZ failover scenarios are needed.

---

## Decision 2 — How is RDS created? (provisioning mechanism)

| Option | Mechanism | Pros | Cons |
|---|---|---|---|
| **A. AWS SDK for Go v2** (recommended) | `aws-sdk-go-v2/service/rds` calls directly from the provider | In-process, no extra tooling, matches Docker provider's direct-client style, easy to thread `ctx`/telemetry | Must hand-roll waiters, tagging, error handling |
| B. Terraform (shell out) | Provider runs `terraform apply/destroy` against an embedded module | Declarative, reusable infra, drift handling | External binary dep; state file lifecycle; slow; awkward to thread per-run identifiers |
| C. CloudFormation | `CreateStack`/`DeleteStack` via SDK | Native, atomic stack teardown | Heavier; slower; template indirection for one instance |
| D. LocalStack | Point the SDK at a LocalStack RDS endpoint | No real cost; fast local tests | LocalStack RDS fidelity is limited; not a real smoke test |

**Recommendation: A (SDK v2)**, mirroring how the Docker provider talks to the Docker client
directly. Optionally support **D (LocalStack)** for free CI by honoring a custom endpoint env
var (`AWS_ENDPOINT_URL`) — it's a few lines in `config.LoadDefaultConfig` and lets the
non-live tests run without real spend.

---

## Decision 3 — Network reachability (so the harness DSN can connect)

RDS is remote; unlike Docker there is no `localhost:port`. This is the part of an AWS provider
that genuinely differs from Docker, so it's worth getting concrete.

### The core problem

With Docker, `WaitForReady` connects to `postgres://postgres:test@localhost:<port>` — the DB
is on the same machine, so reachability is free. With RDS, the provider gets back a **DNS
endpoint** like `dbtest-abc123.c9xyz.us-east-1.rds.amazonaws.com:5432`, and three separate
things must all be true before the harness's `pgx.Connect` succeeds:

1. The endpoint **resolves to an IP you can route to**.
2. That IP sits in a **subnet with a network path** back to your machine.
3. A **security group** explicitly allows inbound TCP 5432 from your source IP.

If any one is missing, the connection just hangs until timeout. So "network reachability" is
really these three layers, and the options below are different ways of satisfying them.

### RDS networking fundamentals

- **Every RDS instance lives in a VPC**, placed into a **DB subnet group** (a named set of
  subnets across ≥2 AZs). If you don't specify one, RDS uses the **default VPC's** subnet group.
- **`PubliclyAccessible` flag** controls DNS resolution:
  - `true` → AWS assigns a public IP; the endpoint resolves to the **public IP from the
    internet** and the private IP from inside the VPC.
  - `false` → no public IP; the endpoint **only resolves to a private IP**, reachable solely
    from inside the VPC (or via VPN / Direct Connect / peering).
- **A public IP alone isn't enough** — the instance's subnet also needs a route to an
  **Internet Gateway** (i.e. it must be a "public subnet"). The **default VPC** that every AWS
  account ships with already has public subnets + an IGW wired up. *This is the single biggest
  reason "public + SG" works out of the box: you're riding on the default VPC's plumbing.*
- **Security group** = the stateful firewall on the instance. RDS denies all inbound by
  default; you must add a rule: `protocol=tcp, port=5432, source=<your IP>/32`.

### The options, concretely

| Option | Setup | Pros | Cons |
|---|---|---|---|
| **A. Public + security group** (recommended default) | `PubliclyAccessible=true`, default VPC, SG allows runner's egress IP on 5432 | Works from laptop and CI out of the box; minimal assumptions | Publicly resolvable endpoint (mitigated: disposable, short-lived, IP-scoped SG) |
| B. VPC-internal only | `PubliclyAccessible=false`; harness runs inside the VPC | Most locked-down | Won't connect from a local machine; requires CI in-VPC |
| C. Configurable (env-driven) | Default A, allow `AWS_RDS_PUBLIC=false` + explicit `AWS_RDS_SECURITY_GROUP_IDS`/`AWS_RDS_SUBNET_GROUP` | Flexible across local + prod-like CI | A bit more config surface |

**A. Public + security group (the default-VPC happy path).** The provider creates the instance
with `PubliclyAccessible=true` and no explicit subnet group (→ default VPC), detects the
runner's current public egress IP (e.g. `GET https://checkip.amazonaws.com`), creates/reuses a
security group with an inbound rule for `<that-IP>/32` on 5432, and attaches it via
`VpcSecurityGroupIds`. It works because the default VPC already routes to the internet (steps
1–2 are free); the provider only handles step 3 (the SG rule). Gotchas:
- **Egress IP can change** (laptop on a new network, CI without a stable NAT). A stale SG rule
  → connections silently time out. Mitigation: detect the IP at provision time, per run.
- **GitHub-hosted CI runners have a wide, changing IP range** — you can't scope to `/32`
  reliably. You'd either widen the SG (worse) or use a self-hosted runner / NAT with a known
  IP. This is option A's main weakness in CI.
- Some orgs **delete the default VPC** by policy, which breaks the happy path entirely → that's
  exactly when you need option B/C.

**B. VPC-internal only.** `PubliclyAccessible=false`, explicit `AWS_RDS_SUBNET_GROUP` (private
subnets) and `AWS_RDS_SECURITY_GROUP_IDS`. The endpoint resolves to a **private IP**, so the
harness must already be **inside the same VPC** — a CI runner on an EC2 instance/ECS task in
that VPC, with an SG rule allowing the runner's SG or private CIDR. Most secure (nothing
internet-facing) but **won't connect from a laptop**, and the provider consumes networking IDs
you supply rather than creating them.

**C. Configurable.** Default to A; if `AWS_RDS_PUBLIC=false`, switch to B using the supplied
subnet group + SG IDs. One code path, two behaviors: zero-config locally, locked-down in
prod-like CI. The only branching is "auto-detect IP + create SG" vs "use the IDs from env."

### What this means for the provider code

```
Provision:
  publicly := envBool("AWS_RDS_PUBLIC", true)
  if publicly:
      ip   := detectEgressIP()            // GET https://checkip.amazonaws.com
      sgID := ensureSecurityGroup(ip)     // create-or-reuse, ingress 5432 from ip/32
      input.PubliclyAccessible  = true
      input.VpcSecurityGroupIds = [sgID]
      // no subnet group → default VPC
  else:
      input.PubliclyAccessible  = false
      input.DBSubnetGroupName   = env("AWS_RDS_SUBNET_GROUP")
      input.VpcSecurityGroupIds = split(env("AWS_RDS_SECURITY_GROUP_IDS"))
```

One subtlety: the **endpoint address isn't populated by `CreateDBInstance`** — it only appears
once `DescribeDBInstances` reports `available`. So DSN construction must happen after the wait,
which is the same reason this plan keeps create-and-wait together inside `Provision`
(see the DSN handoff note under Decision 1 / the implementation files).

**Recommendation: C — default to A, allow B via env.** Disposable test instances make public
access acceptable, while the env override keeps a VPC-internal path open for stricter
environments. Auto-creating/scoping the SG to the runner's current egress IP keeps the blast
radius small.

---

## Decision 4 — Mapping `ProvisionRequest` → RDS instance class

`ProvisionRequest` is `{VCPU, MemoryMiB, DiskGiB}`, but RDS takes an instance *class*
(e.g. `db.t3.medium`), not arbitrary CPU/memory. (The Docker provider maps the same struct to
cgroup limits in `dockerResources`, `provider/docker/docker.go:94-106`.)

| Option | Logic | Pros | Cons |
|---|---|---|---|
| **A. Curated lookup table** | Nearest class from a small list (t3.small/medium/large…) | Honors the scenario's declared "how much"; predictable cost | Need to maintain the table |
| B. Env-fixed class | Ignore VCPU/Mem; read `AWS_RDS_INSTANCE_CLASS` (default `db.t3.medium`) | Dead simple | Ignores the resource spec entirely |
| C. **Table + env override** (recommended) | Default to A; `AWS_RDS_INSTANCE_CLASS` forces a class | Honors spec by default, escape hatch for special cases | Slightly more code |

`DiskGiB` → `AllocatedStorage`, floored at the RDS minimum (20 GiB for gp2/gp3). A `0` disk
request should default to 20.

**Recommendation: C.**

---

## Decision 5 — Credentials & region

| Option | Source | Notes |
|---|---|---|
| **A. Default credential chain** (recommended) | `config.LoadDefaultConfig`: env vars → shared profile → IAM role/instance profile | Standard AWS practice; zero special handling; works in CI via OIDC role |
| B. Explicit keys via env | Read `AWS_ACCESS_KEY_ID`/`SECRET` directly | Redundant with A; discourages roles |

Region from `AWS_REGION` (the SDK reads it too). **Recommendation: A.** Master DB password is
generated per-instance (random UUID) — never hard-coded, and (per Decision 8) carried
separately from the connection target rather than baked into a persisted DSN.

---

## Decision 6 — Failure injection (optional `FailureInjector`)

The Docker provider SIGKILLs and restarts Postgres (`provider/docker/docker.go:178-215`).

| Option | RDS equivalent | Pros | Cons |
|---|---|---|---|
| **A. Defer** (recommended for v1) | Not implemented yet | Smaller first PR; scenarios needing it just don't run on AWS | Parity gap with Docker |
| B. `RebootDBInstance(ForceFailover=true)` | Forces an AZ failover (Multi-AZ) or reboot | Realistic managed-DB failure | Requires Multi-AZ for true failover; minutes-long |
| C. Stop/Start instance | `StopDBInstance`/`StartDBInstance` | Simulates an outage window | Slow; cold-start semantics differ from a crash |

**Recommendation: A now, B as a follow-up.** Leave a compile-time seam (`var _
provider.FailureInjector = ...`) commented or stubbed so adding it later is mechanical.

---

## Decision 7 — Cleanup & cost safety

Real instances cost money and can leak. The existing `state/cluster.go` already records every
cluster row (provider name + status) for exactly this reason.

- Tag every instance `dbtest=true` (+ scenario name) so orphans are identifiable.
- `DeleteDBInstance` with `SkipFinalSnapshot=true`, `DeleteAutomatedBackups=true`.
- Deprovision retries on transient errors (mirror `Deprovision` in
  `provider/docker/docker.go:131-161`).
- Follow-up: an orphan-sweeper that lists `dbtest=true` RDS instances and reconciles against
  the `clusters` table (catches crash-before-deprovision).

---

## Decision 8 — Connection topology: structured `PGTarget` vs DSN string

Today `provider.ClusterInfo` carries a single opaque DSN **string**, consumed in four places:
`pgx.Connect` in the Docker provider's `WaitForReady` (`provider/docker/docker.go:115`),
`pgadapter.Connect` + the workload in `scenario/steps.go:61,130,162`, and — crucially — it is
**persisted into the `clusters.dsn` column** (`state/cluster.go:30`). Because the password is
embedded in the DSN, it is currently written to the state DB in plaintext.

An alternative (borrowed from the `pg-telemetry-lab` `topology` package) is a structured,
provider-agnostic target with the secret held separately:

```go
// provider/provider.go
type PGTarget struct {
    Host     string
    Port     int
    Database string
    User     string
}

func (t PGTarget) Addr() string { return fmt.Sprintf("%s:%d", t.Host, t.Port) }

// URL renders a pgx/libpq URL DSN with the password injected at use-time.
func (t PGTarget) URL(password string) string {
    return fmt.Sprintf("postgres://%s:%s@%s:%d/%s",
        t.User, password, t.Host, t.Port, t.Database)
}

type ClusterInfo struct {
    ID       string
    Target   PGTarget
    Password string // secret — never persisted; rendered into a DSN only at connect time
}
```

| Option | What it is | Pros | Cons |
|---|---|---|---|
| **A. Keep DSN string** | `ClusterInfo{ID, DSN}` as today | Zero refactor | Password persisted in `clusters.dsn`; provider hand-assembles DSNs; no structured host/port for SG rules/logs |
| **B. Trimmed `PGTarget`** (recommended) | `Host/Port/Database/User` + `Addr()` + `URL()`, password separated | Keeps secret out of the state DB; RDS maps `Endpoint.Address`/`Port` straight in; provider returns data, consumer renders the DSN; `pgx`/`pgxpool` accept the rendered URL unchanged | Contract change to `ClusterInfo` ripples to ~5 files |
| C. Full `pg-telemetry-lab` port | Also `Label` + multiple targets + `ConnInfo` (libpq keyword form) | Ready for replicas/replication | `ConnInfo` is motivated by `CREATE SUBSCRIPTION`; `Label`/multi-target by writer/reader topology — dbtest has neither yet (premature) |

**Recommendation: B.** Adopt the trimmed `PGTarget` now; defer `ConnInfo` and
`Label`/multi-target until an Aurora provider or a logical-replication scenario actually needs
them. RDS makes this *cleaner*, not messier: `DescribeDBInstances` returns `Endpoint.Address`
→ `Host` and `Endpoint.Port` → `Port`, so the provider returns structured data instead of
`fmt.Sprintf`-ing a DSN, and the consumer renders the URL at connect time.

**Touch-list (small; the state DB is disposable, so the schema change is free — no migration):**

1. `provider/provider.go` — add `PGTarget`, reshape `ClusterInfo` (`Target` + `Password`).
2. `provider/docker/docker.go` — return `Target`+`Password`; `WaitForReady` uses
   `cluster.Target.URL(cluster.Password)`.
3. `provider/aws/aws.go` — populate `Target` from the RDS endpoint.
4. `scenario/steps.go` (×3) — `rc.Cluster.Target.URL(rc.Cluster.Password)`.
5. `state/cluster.go` + `state/state.go` schema — store `host/port/database/user` columns,
   **not** the password (drop the `dsn` column).

This should land **before** the AWS `Provision` body is finished, so the provider is written
against the new shape rather than the DSN-string shape and immediately rewritten.

---

## Recommended starting configuration (summary)

- **Provision:** single RDS Postgres instance via **AWS SDK v2** (`CreateDBInstance`).
- **Network:** public + IP-scoped security group, `AWS_RDS_PUBLIC=false` override for VPC.
- **Sizing:** curated `ProvisionRequest`→class table, `AWS_RDS_INSTANCE_CLASS` override,
  `DiskGiB`→`AllocatedStorage` floored at 20.
- **Creds/region:** default credential chain + `AWS_REGION`; random per-instance master password.
- **Failure injection:** deferred (seam left for reboot-with-failover).
- **Cleanup:** tag `dbtest=true`, skip final snapshot, retry teardown, orphan-sweeper later.
- **Connection topology:** trimmed `PGTarget` (structured host/port/db/user + separated
  password) — see Decision 8.
- **Optional:** `AWS_ENDPOINT_URL` to point at LocalStack for free, non-live CI runs.

### New / changed files this implies
- `provider/aws/aws.go` — the provider (mirrors `provider/docker/docker.go`).
- `provider/aws/sizing.go` — `ProvisionRequest`→class mapping + env override.
- `provider/aws/sizing_test.go` — unit tests for the mapping.
- `provider/provider.go` — add `PGTarget`, reshape `ClusterInfo` (Decision 8).
- `provider/docker/docker.go`, `scenario/steps.go`, `state/cluster.go`+`state/state.go` —
  adapt to the `PGTarget`/separated-password shape (Decision 8).
- `go.mod`/`go.sum` — add `aws-sdk-go-v2/config` and `aws-sdk-go-v2/service/rds`; `go mod tidy`.

`telemetry/` and `cmd/runbenchmark/` need no changes — the registry (`provider.Run`) and
provider-labelled metrics already support a new provider. (The earlier "no changes to
`scenario/`/`state/`" note holds only if you keep the DSN-string contract from Decision 8
option A.)
