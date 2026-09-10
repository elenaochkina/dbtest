# Plan: the disruption API

`provider.FailureInjector` cannot express what managed providers do, and AWS does
not implement it. Until it is replaced there is no code path from a workflow to
`RebootDBInstance`, which means **no cloud run of the downtime experiment can
happen at all** — the workflow would provision an RDS instance, start a probe,
measure a flat line, and tear it down.

Status: nothing built. Not touched by PR #18 or PR #20.

## 1. What exists

```go
type FailureInjector interface {
	KillProcess(ctx context.Context, cluster ClusterInfo) (ClusterInfo, error)
}
```

One method, one meaning: SIGKILL the database process. Only `provider/docker`
implements it. `ProviderActivities.KillProcess` does a type assertion that fails
for AWS, so the activity errors out before it reaches the API.

## 2. Why it cannot be extended to AWS

**RDS has no ungraceful kill.** What AWS offers is `RebootDBInstance`, and
`RebootDBInstance` with `ForceFailover` on Multi-AZ. Neither is a process kill.
Implementing `KillProcess` for AWS would give the method a name that lies.

**Restart and failover are different measurements, not settings of one.** A
restart returns the same endpoint pointing at the same instance. A failover
returns the same endpoint pointing at a *different* instance, after a DNS change.
One method cannot say which was asked for, and a downtime number that does not
record which disruption produced it is not comparable to any other number.

**There is no way to ask what a provider can do.** A missing capability surfaces
when the call fails, which is after provisioning. Discovering "aws cannot crash"
after creating an instance and running a load phase costs ten minutes and real
money; discovering it in the first activity costs nothing.

**Capability depends on topology, not just on the provider.** Failover works on a
Multi-AZ RDS instance and not on a single-AZ one. Same provider, same code,
different answer — so the question needs the cluster.

## 3. The replacement

```go
type Disruption string

const (
	Restart  Disruption = "restart"  // graceful; every provider has one
	Failover Disruption = "failover" // requires an HA topology
	Crash    Disruption = "crash"    // ungraceful; docker, maybe Aurora
)

// Supports reports whether this provider can perform d against this cluster.
Supports(cluster ClusterInfo, d Disruption) bool

// Disrupt applies d and returns refreshed connection info.
Disrupt(ctx context.Context, cluster ClusterInfo, d Disruption) (ClusterInfo, error)
```

`Supports` exists as a separate method so the workflow can check every disruption
it intends to apply **before provisioning**, in its first activity.

`Disrupt` returns `ClusterInfo` because Docker's published host port is reassigned
on every container start. RDS keeps a stable endpoint DNS name across both reboot
and failover, so the AWS implementation returns the cluster unchanged — but the
signature has to carry it for Docker's sake.

### 3.1 Open decision: optional capability, or part of `Provider`?

The original plan made `Disruptor` an optional interface satisfied by type
assertion, mirroring `FailureInjector`:

```go
d, ok := p.(provider.Disruptor)
```

That leaves two separate "can it?" mechanisms — whether the provider implements
the interface, and what `Supports` answers.

The alternative is folding both methods into `Provider`. Every provider in §4 has
a restart; there is no row where the Restart column is empty. An optional
interface that nothing declines is not optional, it is indirection. Folding it in
gives one mechanism instead of two, removes the assertion and the "cannot be
disrupted" error path from every call site, and makes it impossible for a new
provider to forget the capability.

The cost is boilerplate: a future mock or bring-your-own-DSN target must implement
both methods to return `false` and an error. Three lines.

**Recommendation: fold into `Provider`.** Decide before the AWS implementation is
written; it is far cheaper now than after three providers exist.

## 4. Support matrix

The interface is identical for every provider. What varies is what `Supports`
returns.

| provider | Restart | Failover | Crash |
|---|---|---|---|
| docker | yes | no | yes |
| aws, single-AZ | yes | **no** | no |
| aws, Multi-AZ | yes | **yes** | no |
| aurora | yes | yes | fault injection — **verify** |
| cloudsql | yes | HA only | no |
| azure flexible | yes | zone-redundant HA only | no |
| neon | yes | no | no |

`aws` appears twice on purpose. That is why `Supports` takes `ClusterInfo`.

## 5. Per-provider implementation

**docker** — `Crash` is the existing `KillProcess` body (SIGKILL the container,
start it again, re-read the reassigned host port). `Restart` is a container
restart. `Failover` is unsupported.

**aws** — the only genuinely new code.

| disruption | call |
|---|---|
| `Restart` | `RebootDBInstance` |
| `Failover` | `RebootDBInstance` with `ForceFailover: true` |
| `Crash` | unsupported |

`Supports` returns true for `Failover` only when the instance is Multi-AZ, which
is readable from the provision request or from `DescribeDBInstances`.

## 6. Two traps on the AWS side

**Do not poll for `available` too early.** `RebootDBInstance` returns immediately
and the instance goes `available` → `rebooting` → `available`. Polling for
`available` straight away sees the *pre-reboot* state and concludes the disruption
finished before it started. Wait for the status to leave `available`, then wait
for it to come back.

This matters more here than in most places: `Disrupt` returning early means the
workflow's settle period starts too soon, so disruption N+1 lands while N is still
recovering — and the probe records one merged outage instead of two.

**Record which disruption ran.** The results row has to carry it, or a restart
number and a failover number end up in the same column and neither means anything.

## 7. Files that change

| file | change |
|---|---|
| `provider/provider.go:53` | `FailureInjector` → `Disruption` + the two methods |
| `provider/docker/docker.go:240` | `KillProcess` becomes `Crash`; add `Restart`; add `Supports` |
| `provider/aws/aws.go` | new — reboot and force-failover |
| `temporal/provider_activities.go:71` | `KillProcess` activity → `Disrupt` activity |
| `temporal/crash_recovery_workflow.go:104` | updated call |
| `scenario/steps.go:94` | `killProcessStep` → `disruptStep` |

Everything except the AWS implementation is renaming and rerouting.

## 8. Order

1. Decide §3.1 — optional or mandatory.
2. `provider/provider.go`, then `provider/docker` so the existing crash path keeps
   working and `Restart` becomes testable locally for free.
3. Update the three call sites; the Docker workflow should still pass end to end.
4. `provider/aws` — reboot first, failover second, since failover needs a Multi-AZ
   instance and costs more to exercise.
5. Check `Supports` in the workflow's first activity, before `Provision`.
