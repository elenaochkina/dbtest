# Plan: the harness API

`harness.Runner` has six methods and every one of them describes Docker.
Fargate — the actual target — cannot honour two of them, and the sequence the
remaining four have to be called in is easy to get wrong. PR #20 gets it wrong in
three places while still compiling.

Replace it with two methods.

Status: nothing built. The interface today has no merged consumers, so reshaping
it is free.

## 1. What is wrong with six methods

```go
type Runner interface {
	Start(ctx, spec) (Handle, error)
	Wait(ctx, h) (int, error)
	Stop(ctx, h) error
	Remove(ctx, h) error
	Output(ctx, h) ([]byte, error)
	Logs(ctx, h) ([]byte, error)
}
```

**`Output` and `Logs` promise something only Docker can deliver.** Docker can
return stdout and stderr separately because `stdcopy` demultiplexes framed
streams. Fargate's `awslogs` driver sends both to one CloudWatch stream with no
marker distinguishing them. Any caller relying on the split works locally and
fails in the cloud.

**`Remove` has nothing to remove on Fargate.** A stopped task lingers in the API
for about an hour and is then reaped. Billing already ended at `STOPPED`.

**The call order is load-bearing and unenforced.** Collecting a result means
`Stop` → `Wait` → `Output` → `Remove`, and any other order silently loses either
the result or the container. PR #20 calls `Wait` before stopping (blocks until
`-max-duration`) and never calls `Remove` (leaks a container per run) — and it
compiles and vets clean.

## 2. The interface

```go
// Runner starts a container somewhere and collects what it printed.
type Runner interface {
	// Run creates and starts the container, returning once it is running.
	Run(ctx context.Context, spec Spec) (Handle, error)

	// Stop ends the container and returns everything it printed. Output is
	// returned even when the error is non-nil.
	Stop(ctx context.Context, h Handle) ([]byte, error)
}
```

`Wait`, `Output`, `Logs` and `Remove` become unexported helpers inside each
implementation. `harness.Run(name, tel)` — the registry lookup — renames to
`harness.New` to free the name.

Three decisions are embedded in that signature.

### 2.1 `Stop` returns combined output

Not stdout. An interface that promises stdout is one only Docker can keep, which
guarantees a class of bug that passes locally and fails on Fargate. The combined
stream is what every backend can actually deliver.

Consequence: `probe.Parse` currently takes the last non-empty line. It must scan
backwards for the last line that **parses as JSON**. That is more robust anyway —
today it is protected only by `fmt.Println` happening to be the last statement in
`main`.

### 2.2 The exit code folds into the error

`Wait` returned `(int, error)`. A non-zero exit now becomes
`fmt.Errorf("container exited %d: %s", code, tail(out))`, so the caller gets a
diagnostic instead of an integer to interpret, and the `tailLogs` assembly PR #20
does by hand disappears.

**Output is returned alongside a non-nil error.** The probe prints its JSON and
then exits; if it exits non-zero the JSON is still wanted.

### 2.3 Self-exiting containers need a flag

This is the one real cost of dropping to two methods.

`cmd/bench` exits on its own after `-duration`. `cmd/probe` runs until stopped.
With no `Wait`, there is no backend-agnostic way to await a natural exit — and
`bench -init` has no predictable duration, so the caller cannot simply sleep.

Encode the lifecycle in the spec and carry it on the handle:

```go
type Spec struct {
	Image     string
	Args      []string
	Name      string
	SelfExits bool // bench exits on its own; probe runs until stopped
}

type Handle struct {
	ID        string
	SelfExits bool // copied from Spec by Run
}
```

`Stop` then branches: if `SelfExits`, wait for exit and collect; otherwise
terminate, wait, collect. `Handle` is opaque to callers — they pass it back.

`Spec.Network` is dropped; see §4.

## 3. Fargate mapping

| method | Fargate |
|---|---|
| `Run` | `RunTask` → poll `DescribeTasks` until `lastStatus == RUNNING` |
| `Stop` | `StopTask` → poll until `STOPPED` → read CloudWatch → exit code from `containers[0].exitCode` |
| `Stop`, `SelfExits` | skip `StopTask`; poll until `STOPPED`, then collect |

No removal.

## 4. Four things that do not exist on Docker

**CloudWatch ingestion lag — the one most likely to bite.** The `awslogs` driver
batches. Lines appear seconds after the container writes them, often after the
task reports `STOPPED`. Reading once at that point can miss the final line, which
is the result.

So `Stop` cannot be "poll until STOPPED, then `GetLogEvents` once". It has to poll
`GetLogEvents` until the result appears or a deadline passes. `GetLogEvents` also
paginates: loop until `nextForwardToken` stops changing, or the last page is
silently dropped. On Docker, `ContainerLogs` is authoritative the instant the
container exits — nothing here has an analogue.

**`Run` must wait for RUNNING.** `RunTask` returns a task ARN immediately; the
task then goes `PROVISIONING` → `PENDING` → `RUNNING`, which is 10–60 s on Fargate
for ENI attachment and the ECR pull. Disrupting before the probe is sampling makes
the outage invisible. `RUNNING` still only means the container started, not that
`Prepare` finished, so the workflow should settle a few seconds before the first
disruption.

**`stopTimeout` lives in the task definition.** On Docker the SIGTERM→SIGKILL
grace is an argument to `ContainerStop`. On Fargate it is `stopTimeout` in the task
definition — default 30 s, max 120 s. Too short and the probe is killed mid-print
and the result is lost silently. Terraform must set it explicitly.

**Networking is per-runner, not per-spec.** `Spec.Network` is a Docker network
name. Fargate needs `awsvpc`: subnets, security groups, `assignPublicIp`. That is
static infrastructure, so it belongs in the runner's construction, read from env
the way `provider/aws/aws.go` already does:

```
AWS_ECS_CLUSTER
AWS_ECS_SUBNETS               comma-separated
AWS_ECS_SECURITY_GROUPS       must reach RDS on 5432
AWS_ECS_LOG_GROUP
AWS_ECS_TASKDEF_PROBE
AWS_ECS_TASKDEF_BENCH
```

`Spec` then stays about the container: which image, which args, what name.

## 5. Terraform prerequisites

None of this exists yet, and no Go work can be exercised in the cloud until it
does:

- ECR repositories for `probe` and `bench`, with images pushed — the Makefile only
  builds local tags today
- ECS cluster
- Two task definitions, each with `stopTimeout`, the `awslogs` driver, and a
  CPU/memory size
- Task execution role (ECR pull, CloudWatch write) and task role
- Task security group, plus an ingress rule on the RDS security group allowing
  5432 from it
- Subnets with egress to ECR and CloudWatch — NAT gateway, or VPC endpoints

This is a larger prerequisite than the harness change itself and is independent of
it, so it can proceed in parallel.

## 6. Workflow sequence

```
Provision RDS                          local worker, RDS API
Run bench task    SelfExits=true       RunTask, wait RUNNING
Run probe task    SelfExits=false      RunTask, wait RUNNING
settle                                 workflow.Sleep
for i in 1..N:
    Disrupt RDS                        RDS API — needs the Disruptor (see disruption-api.md)
    settle                             workflow.Sleep
Stop probe                             StopTask + collect → probe.Result
Stop bench                             collect → bench result
Save results                           local state DB
Deprovision RDS                        deferred
```

**`Run` blocks for up to a minute**, so its activity needs a generous
`StartToCloseTimeout` and should heartbeat. PR #20 already has a `heartbeat`
helper.

**`Stop` is call-once.** On Fargate it is naturally retry-safe — `StopTask` on a
stopped task is fine and reading logs does not consume them. On Docker it is not,
because it removes the container. The contract has to hold for both, so declare it
call-once and set `MaximumAttempts: 1` on the stop activities. PR #20's current
`CollectProbe` comment reasons the opposite way and needs rewriting: under the new
contract, stopping is what makes the result exist.

## 7. Files that change

| file | change |
|---|---|
| `harness/harness.go` | two-method `Runner`; `Spec.SelfExits`; `Handle.SelfExits`; drop `Spec.Network`; `Run` → `New` |
| `harness/docker/docker.go` | implement the two methods, unexport the rest, keep PR #20's adopt-on-conflict logic |
| `harness/fargate/fargate.go` | new |
| `probe/result.go` | `Parse` scans backwards for the last JSON-parseable line |
| PR #20 activities | `CollectProbe` and `StopProbe` collapse into one; same for bench; drop `-repetitions` |

## 8. Order

1. `harness/harness.go` and `harness/docker`, plus `probe.Parse`. One commit.
2. Collapse PR #20's activities onto the new interface.
3. One full workflow against Docker — free, and it proves the sequence before any
   of it costs money.
4. Terraform and the ECR push (§5), in parallel from the start.
5. `harness/fargate`, written against a contract already proven on Docker. Its one
   genuinely new piece is the log-lag retry.

## 9. One thing given up

Two methods make the harness untestable in isolation. `Stop` does four things and
the only way to observe them is against a real backend; the six-method shape at
least let each piece be checked.

That is not a reason to keep six. It is a reason to want one integration test that
runs a trivial container — `Run`, `Stop`, assert the output — because after this
change the harness has exactly one observable behaviour and no unit-level seams.
It is also the test most likely to catch a Fargate implementation drifting from
the Docker one.
