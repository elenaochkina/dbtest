# Terraform + Fargate: build steps

Successor to the Terraform and orchestration parts of
[fargate-benchmark.md](fargate-benchmark.md). Steps and verification gates only.

Branch `terraform`, cut from `disruption-api`. Stages 1 and 2 are pure AWS infra
and depend on no Go code, so they do not wait on PR #21. Stage 3 needs
`harness.Runner`, which this branch already carries.

## Supersedes

`fargate-benchmark.md` predates the harness restructure. Three of its sections
are no longer the plan:

- **§10 `RunFargateBenchmark` activity.** `harness.Runner` (Run/Stop) now exists
  and `harness/docker` implements it. Fargate becomes a second implementation,
  so `RecoveryWorkflow` runs unchanged with `runner = fargate`. No new workflow,
  no new activity.
- **§5, §8, §14 `cmd/benchrunner`.** `cmd/bench` and `cmd/probe` already exist
  and build as containers. Two containers, not one.
- **§15 result transport.** Closed. `Stop` returns merged stdout and stderr and
  `probe.Parse` scans backwards for the last JSON-parseable line — a contract
  written for CloudWatch. Not S3.

`terraform-deployment.md` describes the ephemeral EC2 path, which is paused. It
is not this.

## Stage 0 — Before anything else

1. **Set a billing alarm and tag every resource.** A forgotten RDS instance bills
   silently, and the failure mode that creates one — killing the worker, which
   skips `defer Deprovision` — happens routinely while developing. Do this before
   the first `terraform apply`, not after.
2. **Networking.** Public subnet with `assignPublicIp=ENABLED` for the first cut.
   RDS stays private; the task reaches ECR and CloudWatch over the IGW. NAT
   gateway and VPC endpoints deferred.
3. **`stopTimeout` per container.** The probe must print its result JSON before
   SIGKILL. Docker uses a 10s grace; in Fargate this lives in the container
   definition, capped at 120s. Bench self-exits and does not need it.
4. **DSN secrecy.** Plain env override via RunTask, or Secrets Manager. The
   instance is throwaway, so plain is defensible — decide it rather than default
   into it.

## Stage 1 — Images into ECR

1. Create ECR repositories for `bench` and `probe`.
2. Add a Makefile target that tags and pushes both, parallel to `make images`.
3. Build for the task architecture. Fargate defaults to x86_64; the dev machine
   is arm64. A mismatch fails at task start, not at build.
4. **Gate:** pull each image back from ECR and run it against a local Postgres.
   Do not proceed until the pulled artifact works.

## Stage 2 — Terraform static infra

1. **Bootstrap remote state first** — S3 bucket and DynamoDB lock table, created
   outside the main config. The config that uses them as a backend cannot create
   them.
2. ECS cluster. Idle cost with no running tasks is ~zero.
3. Two task definitions, one per container, differing in `stopTimeout`, log
   configuration, and CPU/memory.
4. Execution role: ECR pull and CloudWatch write. Task role stays empty — RDS
   permissions belong to the worker, not the task.
5. Task security group, plus an ingress rule on the RDS security group allowing
   5432 from it.
6. CloudWatch log group with short retention.
7. **Decide how Terraform outputs reach the worker.** `provider/aws` reads
   `AWS_REGION`, `AWS_RDS_SUBNET_GROUP`, `AWS_RDS_SECURITY_GROUP_IDS` and
   `AWS_RDS_PUBLIC` from the environment. Nothing carries new resource IDs there
   after an apply, so the worker keeps using stale ones until someone updates it.
   Pick a mechanism — `terraform output` written to a file the worker sources is
   enough.
8. **Gate:** launch a task with `aws ecs run-task` by hand. Confirm it reaches
   RUNNING, writes logs, and reaches STOPPED with exit 0. Prove this before
   writing any Go.

## Stage 3 — harness/fargate

Implement `Run` and `Stop` against the existing interface. Four known traps:

1. **`Run` must wait for RUNNING**, not just accept the RunTask response. A task
   can sit in PROVISIONING for tens of seconds.
2. **`Stop` must retry log retrieval.** CloudWatch ingestion lags task exit. A
   single fetch returns truncated output or nothing, and `probe.Parse` then fails
   on a run that succeeded.
3. **`SelfExits` splits the paths.** Bench waits for exit; the probe needs an
   explicit StopTask.
4. **Double-stop tolerance**, matching the Docker runner — `RecoveryWorkflow`
   stops the probe in both a defer and the main path.

**Gate:** drive the probe and bench containers through this runner directly
against a Postgres, outside Temporal.

## Stage 4 — Wire in

1. Extend `runnerFor` to return the Fargate runner.
2. Add runner selection to `cmd/starter`.
3. Raise `-timeout` and `-write-timeout` from their 100ms and 500ms defaults
   before the first AWS run. A healthy sample against RDS needs roughly six round
   trips — DNS, TCP, TLS, SCRAM, query — and will otherwise be recorded as a
   failure.
4. First run is `-workflow recovery -provider aws -disruption restart`. Not
   failover: `Supports` refuses it and `provider/aws.Provision` never sets
   `MultiAZ`.

## Stage 5 — Cost hygiene

1. Confirm `defer Deprovision` fires against a real RDS instance, including on
   failure paths. This is the expensive one to get wrong.
2. Add a `terraform destroy` path and confirm nothing survives it.

## Ordering

Stages 1 and 2 are the long pole and independent of all Go work. Stage 3 cannot
be meaningfully tested until Stage 2's gate passes.

## Carried gaps

Not Fargate work, but each will present as a Fargate bug when it bites:

- `provider/aws.Provision` never sets `MultiAZ`, so `HighAvailability` is inert
  and failover cannot be tested.
- `-timeout 100ms` is too short for a remote handshake. See Stage 4.3.
- `RecoveryWorkflow` has no execution timeout; a wedged AWS control-plane call
  waits indefinitely.
- `cmd/starter` does not validate `-repetitions >= 1`.
- The probe stops itself after an hour. `cmd/probe/main.go` defaults
  `-max-duration` to 1h and `StartProbe` never sets `MaxDuration`, so the default
  applies. Three repetitions of 17m `Disrupt` plus 5m `WaitForReady` plus settle
  can exceed that, and `StopProbe` then finds a container that already exited.
- `waitForReboot` waits up to 17m (2m to leave available, 15m to return) inside an
  activity whose `StartToCloseTimeout` is 20m with `MaximumAttempts: 1`. The two
  numbers were set independently and leave three minutes of headroom.
