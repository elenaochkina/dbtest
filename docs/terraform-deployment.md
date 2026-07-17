# Plan: Terraform Deployment (ephemeral EC2 runner)

## Context

The in-region benchmark ([in-region-benchmark.md](in-region-benchmark.md)) needs the harness
to run on an EC2 box inside the RDS VPC. The preference is **ephemeral**: create the EC2 (and
its supporting infra) on demand, run the benchmarks, then destroy everything — so nothing bills
between sessions. Terraform expresses this disposable stack as code: `terraform apply` to stand
it up, `terraform destroy` to remove it as one unit (no orphaned instance/SG left billing).

## State DB: yes, recreated from scratch each session

The state DB runs as a Docker container **on the EC2**. Because the EC2 is destroyed each
session, that container — and all `benchmark_results` in it — goes with it. So:

- Every `apply` starts with an **empty** state DB.
- **Results must be exported to a durable store (S3) before `destroy`**, or they're lost.
- This is consistent with the project's "state DB is disposable" stance — the state DB is
  per-run bookkeeping; benchmark *history* lives in S3, not in the throwaway DB.

> Alternative (deferred): make the state DB a small **persistent RDS** so rows accumulate
> across runs and no export is needed. More standing infra/cost; revisit if trend history
> becomes important. For now: ephemeral state DB + export to S3.

## What Terraform manages (the stack)

- **`aws_instance`** — `t3.micro` (x86_64) or `t4g.micro` (arm64), in the us-east-2 default VPC.
- **`aws_iam_role` + `aws_iam_instance_profile`** — RDS permissions
  (`rds:CreateDBInstance/DescribeDBInstances/DeleteDBInstance/AddTagsToResource`) so the harness
  authenticates via instance metadata — **no static keys on the box**. Add `s3:PutObject` for
  the results bucket.
- **`aws_security_group` (EC2)** — egress all; no inbound needed if using SSM.
- **`aws_security_group` (RDS)** — inbound TCP 5432 from the EC2 SG (the VPC-internal path);
  passed to the harness via `AWS_RDS_SECURITY_GROUP_IDS`.
- **`user_data`** — installs Docker, fetches/builds `runbenchmark`, runs the scenarios, exports
  results to S3.
- **`aws_s3_bucket` (results)** — durable home for exported `benchmark_results`.

## Lifecycle (one session)

```
terraform apply
   └─ EC2 + IAM profile + SGs come up
EC2 (via user-data or SSM):
   docker run state DB  →  runbenchmark -provider aws  →  runbenchmark -provider docker
   →  dump benchmark_results to JSON  →  aws s3 cp to results bucket
verify results in S3
terraform destroy
   └─ EC2 + SGs + role removed; nothing left billing
```

The latency-sensitive harness runs **on the EC2** (in-VPC, next to RDS). Terraform/CI only
orchestrates; they never connect to RDS.

## Remote state bootstrap (chicken-and-egg)

Terraform state lives in **S3** (not the repo), with a **DynamoDB** lock table. The bucket +
table that *hold* state can't be created by the same config that *uses* them as a backend.
Create them once — by hand or a tiny separate `infra/bootstrap/` config — then point
`backend.tf` at them and `terraform init`.

## Repo layout & .gitignore

```
infra/
  main.tf variables.tf outputs.tf backend.tf
  user-data.sh
  .terraform.lock.hcl        # provider lock — DO commit (like go.sum)
```

`.gitignore` additions:
```gitignore
infra/.terraform/
*.tfstate
*.tfstate.backup
*.tfvars        # only if they hold secrets
```
Real state never enters git — it's in S3.

## Driving the benchmark on the EC2

- **SSM Run Command** (recommended): `apply` creates the box; send the benchmark command, wait,
  capture output; then `destroy`. No SSH, no public IP, no inbound port 22.
- *Alternative:* user-data runs the benchmark on boot and writes a done-marker + results to S3;
  poll S3 for completion. More loosely coupled, harder to capture logs.

## Cost safety

- `destroy` removes everything Terraform created — no orphan EC2/SG.
- The EC2's disk only bills while it exists; after `destroy` there's zero spend (vs stop/start,
  where the disk lingers).
- `t3.micro` is free-tier eligible.
- (In CI) run `destroy` in an `always()` cleanup step so a failed run never leaks an instance.

## Optional follow-up: CI/CD

A GitHub Actions `workflow_dispatch` (manual — it costs money) that assumes an AWS role via
**OIDC** (no stored keys), then `apply → SSM run → fetch S3 results → destroy (always())`. The
GitHub runner stays outside the VPC; only the EC2 it creates runs the in-region harness.

## Verification

- **After apply:** `aws ec2 describe-instances` shows the instance `running`; SSM connectivity works.
- **After run:** a results object exists in the S3 bucket; it contains the session's `aws` + `docker` rows.
- **After destroy:** `describe-instances` shows `terminated`; no leftover RDS instances; the SGs are gone.
