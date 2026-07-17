# Plan: In-Region Benchmarking (eliminate the WAN round-trip)

## Context

The first live RDS run benchmarked at **6 tps / 652 ms** vs Docker's **2876 tps / 1.4 ms** —
a ~480× gap. That gap is **network distance, not database power**: the benchmark client (your
laptop) reached RDS in us-east-2 over the public internet, and pgbench is *latency-bound* —
each transaction is several SQL statements, and every statement pays a full WAN round-trip
(~50–80 ms). The latency and tps ratios track each other almost exactly, the signature of a
latency-bound workload.

So the measurement is invalid for comparing *databases*. To get a fair number, the client and
the database must be **co-located in the same region** (ideally same AZ), turning ~50 ms
round-trips into ~0.3–0.5 ms.

## Goal

Run `cmd/runbenchmark` **from inside the RDS region/VPC** so the AWS result reflects database
throughput, not internet latency — and (optionally) run the Docker provider from the same host
so both providers are measured from one vantage point.

## Approach: co-locate the runner on an EC2 instance in the same VPC

| Where to run the harness | Verdict |
|---|---|
| **EC2 in the RDS VPC** | ✅ Recommended — full control, free-tier `t3.micro`/`t4g.micro`, can run both providers, IAM instance role for creds |
| AWS CloudShell | ❌ Ephemeral, not in your VPC, awkward Go toolchain |
| ECS/Fargate task | ◻︎ Good for later automation; more setup than a one-off needs |
| Lambda | ❌ Not suited to a long-running pgbench client |

Everything below assumes a throwaway EC2 box in the **us-east-2 default VPC** (where the test
SG already lives).

## Steps

### 1. (Optional, for same-AZ fairness) pin the RDS availability zone
RDS currently picks its own AZ, and the provider doesn't set one. Cross-AZ within a region adds
~1 ms RTT; same-AZ is ~0.3 ms. Same-VPC cross-AZ is already ~100× better than WAN and is
probably "good enough," but for the tightest comparison add one field:

```go
// awsConfig: AvailabilityZone string // AWS_RDS_AVAILABILITY_ZONE
if p.cfg.AvailabilityZone != "" {
    input.AvailabilityZone = aws.String(p.cfg.AvailabilityZone)
}
```
Then launch the EC2 in that same AZ. **Skip this if cross-AZ is acceptable.**

### 2. Networking — switch to the VPC-internal path
With the client inside the VPC, RDS no longer needs to be public:
- Set **`AWS_RDS_PUBLIC=false`** → RDS gets a private endpoint only (more secure, no internet
  exposure).
- Create an RDS security group whose inbound rule allows **TCP 5432 from the EC2's security
  group** (a security-group-referencing rule, not a CIDR) and pass it via
  `AWS_RDS_SECURITY_GROUP_IDS`. EC2 ↔ RDS in the same VPC then connect over private IPs.
### 3. Provision the EC2 runner

- **Instance:** `t3.micro` (x86_64) or `t4g.micro` (arm64), in the us-east-2 default VPC, in
  the chosen AZ (step 1).
- **Credentials:** attach an **IAM instance role** with the RDS permissions
  (`rds:CreateDBInstance/DescribeDBInstances/DeleteDBInstance/AddTagsToResource`). The
  provider's default credential chain picks it up automatically — **no static keys on the
  box.**
- **Access:** SSM Session Manager (no SSH key / open port 22) or a normal SSH key pair.
- **Software:** install Docker (needed if you also want the Docker provider's local Postgres
  and the state DB container).

### 4. Deliver the binary
Cross-compile locally — pgx and the Docker SDK are pure Go, so `CGO_ENABLED=0` yields a static
binary. Match `GOARCH` to the instance (`amd64` for t3, `arm64` for t4g):

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o runbenchmark ./cmd/runbenchmark
# copy via SSM or scp
```
(Alternative: install Go on the EC2 and `git clone` + build there — simpler, slower.)

### 5. State DB on the EC2
The harness needs `STATE_DSN`, but result storage isn't latency-sensitive. Simplest: run the
state Postgres locally on the EC2 —
```bash
docker run -d --name dbtest-state -e POSTGRES_PASSWORD=test -p 5433:5432 postgres:16
# STATE_DSN='postgres://postgres:test@localhost:5433/postgres'
```

### 6. Run both providers from the EC2
```bash
# AWS — now over the private intra-VPC network
AWS_REGION=us-east-2 \
AWS_RDS_PUBLIC=false \
AWS_RDS_SECURITY_GROUP_IDS=<rds-sg-allowing-ec2-sg> \
AWS_RDS_INSTANCE_CLASS=db.t3.micro \
STATE_DSN='postgres://postgres:test@localhost:5433/postgres' \
./runbenchmark -provider aws -scenario benchmark < /dev/null

# Docker — local container on the same EC2 (in-region baseline)
STATE_DSN='postgres://postgres:test@localhost:5433/postgres' \
./runbenchmark -provider docker -scenario benchmark < /dev/null
```

### 7. Compare
```sql
SELECT provider, round(tps::numeric,1) tps, round(latency_avg_ms::numeric,2) latency_avg_ms
FROM benchmark_results ORDER BY created_at DESC;
```
The AWS tps should jump by ~2 orders of magnitude versus the laptop run.

## Validity caveats

- **Localhost still has an edge.** Even intra-AZ (~0.3 ms), RDS-over-network won't match a
  Docker Postgres on `localhost` (~0.05 ms). The honest framing is "managed Postgres reached
  over the VPC network" vs "local container" — not identical conditions, but both free of the
  WAN tax. For a stricter match, run the Docker Postgres on a *separate* in-AZ host too (likely
  overkill).
- **Re-tune pgbench.** Once you're no longer latency-bound, bump `-clients` and `-duration` —
  you're now measuring real throughput, and short/low-concurrency runs under-utilize the DB.
- **Compare like sizes.** If the goal is DB power, use the same instance class on both sides
  rather than `db.t3.micro`.

## Cleanup

- The harness deletes the RDS instance on scenario exit (verify with `describe-db-instances`).
- **Terminate the EC2** when done — it bills hourly even idle.
- Remove the temporary security group(s) if not reused.

## Optional: automate it
Bake steps 4–6 into the EC2 **user-data** script so the box self-provisions, runs both
benchmarks on boot, writes results to a state DB (or a small RDS), and optionally
self-terminates. Turns the manual run into one `RunInstances` call — a good follow-up once the
manual path is proven.
