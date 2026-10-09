# RDS Multi-AZ failover: observations

Measurements from forced failovers between 2026-09-28 and 2026-10-03, on
`db.t3.micro` and `db.m6g.large`.

## Setup

- RDS PostgreSQL 16.13, Multi-AZ, us-west-2, created per run
- Disruption: `RebootDBInstance` with `ForceFailover: true`
- Probe: one Fargate task in the same VPC, fresh connection every 250 ms,
  300 ms connect+read deadline, 200 ms write deadline
- Two availability levels per sample: `SELECT` (readable) then `UPDATE` (writable)
- Seeded with pgbench scale 100; no concurrent load beyond the probe
- `-settle 90s` until 2026-10-02, then `-settle 15m` and `20m`

## Headline numbers

| run | class | failover | takeover (write) | takeover (read) | read/write gap |
|---|---|---|---|---|---|
| A, 09-28 | t3.micro | cold | 37.0 s | 32.8 s | 4259 ms |
| B, 10-01 | t3.micro | cold | 33.7 s | 33.2 s | 501 ms |
| C, 10-01 | t3.micro | cold | 35.2 s | 33.7 s | 1505 ms |
| C, 10-01 | t3.micro | warm | 16.8 s | 16.3 s | 501 ms |
| 3c8ecac0 | t3.micro | cold | 34.5 s | 34.0 s | 501 ms |
| 9b2f641e | t3.micro | cold | 34.8 s | 29.8 s | 5010 ms |
| 9b2f641e | t3.micro | warm | 17.9 s | 17.9 s | 0 ms |
| aws-10 | t3.micro | cold | 29.0 s | 26.9 s | 2116 ms |
| aws-11 | t3.micro | cold | 20.5 s | 18.7 s | 1754 ms |
| aws-11 | t3.micro | warm | 18.0 s | 14.7 s | 3260 ms |
| aws-12 | m6g.large | cold | 20.1 s | 15.9 s | 4260 ms |
| aws-13 | m6g.large | cold | 18.0 s | 15.5 s | 2506 ms |
| aws-13 | m6g.large | warm | 18.2 s | 13.5 s | 4768 ms |
| aws-14 | m6g.large | cold | 19.7 s | 15.0 s | 4760 ms |

One more `t3.micro` run is not in the state DB (its save failed before
f66136e): 39.1 s cold, 20.7 s warm, writable.

For comparison, same harness: RDS single-AZ reboot 13.8 / 10.3 / 7.8 s,
Docker crash 0.45-0.51 s.

**`lost_commits` was 0 in every failover.** No acknowledged write was lost.

## 1. Takeover takes 15-39 s, and the instance class matters

On `db.t3.micro` writable takeover ranged from 16.8 to 39.1 s. A second
failover of the same instance was roughly half the first (0.48, 0.51, 0.53,
0.88), so a first failover there is close to a worst case.

On `db.m6g.large` it ranged from 18.0 to 20.1 s across four failovers, and
cold and warm were the same (18.0 and 18.2 s in one run). The cold/warm gap
looks like a property of the burstable class rather than of failover; this is
an inference from one `m6g.large` run with two failovers.

On `m6g.large` every takeover failure was a timeout. On `t3.micro` some also
included `refused` and `57P03` (the database is starting up), so the probe
reached the new host before Postgres was ready.

## 2. Writes fail before reads, and reads stop exactly at "failover started"

Writes stop first; reads keep working until AWS logs `Multi-AZ instance
failover started`, then both go. Both levels recover on the same sample.

The read failure matches AWS's "failover started" event to within 0.15 s in
all eight failovers where both were recorded. The write-only gap before it
varies from 0 to 5010 ms with no pattern by class, run or repetition.

Run A, the clearest case:

```
21:56:55.793  writes start failing
21:56:59.977  AWS: "Multi-AZ instance failover started"
21:57:00.052  reads start failing          <- 4.26 s later
21:57:32.577  both recover                  (same sample)
```

**Inference, not established:** writes stop when the old primary's storage
stops acknowledging, while reads of cached pages keep working until the host
is fenced.

## 3. Almost none of the takeover is spent in Postgres

aws-14 had Postgres logging enabled on both hosts:

```
19:53:24.776  writes start failing (probe)
19:53:29.537  reads start failing (probe)
19:53:29.609  AWS: "Multi-AZ instance failover started"
19:53:39      new primary: "database system was interrupted; last known up at 19:50:35"
19:53:39      redo 0.01 s, end-of-recovery checkpoint 0.03 s, ready to accept connections
19:53:40.102  AWS: "DB instance restarted"
19:53:44.264  probe recovers, both levels
19:53:49.451  AWS: "Multi-AZ instance failover completed"
```

Of the 15 s read outage, about 10 s passed before Postgres started on the new
host, crash recovery took a fraction of a second, and the probe's first success
came about 5 s after Postgres was ready. The old primary is not shut down
cleanly: the new primary starts with crash recovery.

So the takeover is AWS's control plane and the endpoint switch, not the
database. It will not shrink with a faster disk or a smaller WAL.

## 4. The standby is invisible to Postgres

aws-14 sampled the primary every 5 s, before and after the failover:

```
synchronous_standby_names   ''
synchronous_commit          on
pg_stat_replication         0 rows
```

No sample ever showed a standby, a replication connection or an `ANY 1 (...)`
setting. A Multi-AZ *instance* replicates below Postgres, so Postgres cannot
report when the standby is in sync. (A Multi-AZ DB *cluster* uses Postgres
replication and would show it.)

## 5. AWS's event log is unusable for timing

`Multi-AZ instance failover completed` always postdates the database being
usable, by 3 to 42 s:

```
run A     +27 s      aws-10    +3 s      aws-12    +4 s
run B     +42 s      aws-11    +12 s     aws-13    +25 s, +32 s
run C     +36 s      aws-11    +16 s     aws-14    +5 s
```

`The user requested a failover` is emitted at completion with the
completion's timestamp, so the console's newest-first order looks inverted.
There is no event for "standby rebuilt" or "high availability restored".

The `failover` event category is reliable for identifying what happened; no
timestamp in it is reliable for timing it.

## 6. The AvailabilityZone field swaps late, by an unpredictable amount

`DescribeDBInstances.AvailabilityZone` eventually reports the new primary,
between about 1 and 9 minutes after the failover was requested. No event marks
the swap. It is a sound correctness check (it proves a failover occurred) and a
poor readiness signal. The workflow uses it to pace repetitions, not as a
metric.

## 7. The sub-second "tail" is not failover recovery

Every run watched for 15 minutes or more saw sub-second outages for 10-20
minutes after takeover. They are not caused by the failover:

- **They also happen before any failover.** Every long baseline had them, on
  instances nothing had touched.
- **They are server stalls on a 5-minute cadence.** With
  `log_min_duration_statement = 100`, aws-14's Postgres log showed statements
  from two clients stalling in the same second every 5 minutes:

  ```
  19:42:19  295 ms     20:04:11  309 ms (and a SELECT at 119 ms)
  19:47:19  359 ms     20:09:12  219 ms
  19:52:20  240 ms     20:14:12  234 ms
  ```

  The failover reset the phase; the new primary is a freshly started server.
- **They are not checkpoints.** aws-14 ran with `checkpoint_timeout = 15min`
  and the stalls stayed 5 minutes apart. Its two timed checkpoints wrote 24 and
  153 buffers with 4-7 ms of sync and caused no probe failure. Earlier runs
  lined up with 5-minute checkpoints only because RDS's default
  `checkpoint_timeout` is also 300 s.
- **The stall is 220-360 ms, not 500 ms.** The probe records one failed sample
  as ~500 ms because samples are 250 ms apart. A stall shows up only when it
  crosses the 200 ms write deadline, which is why runs had anywhere from one
  to six blips.
- **Some blips are not the server at all.** A probe failure with no matching
  slow statement in the Postgres log is in the connection path; the probe opens
  a new TLS connection inside a 300 ms deadline every sample.

**Candidate, not established:** RDS sets `archive_timeout = 300`, forcing a
WAL segment switch every 5 minutes. Each stall window shows one WAL fsync
100-150 ms slower than usual in `pg_stat_wal`. Postgres does not log segment
switches, so this is timing, not proof, and a single segment switch stalling
commits past 200 ms would be surprising.

The earlier readings of this tail are withdrawn: the write-only blip in run B
is not evidence of the primary waiting on its standby, and the per-minute
stalls in run C were never shown to depend on the pair being degraded.

**Consequence for the results table.** `sync_ended_at` and `full_recovery_ms`
record the last outage in a disruption's window. On RDS Multi-AZ that is
whichever background stall last crossed a deadline before the window closed,
so the values (614 s to 1248 s at `-settle 15m` or more) measure the window,
not recovery. The columns are kept until Aurora shows whether another topology
gives them a meaning.

## Measurement caveats

**"Outage" includes "slow".** The probe allows 300 ms to connect and read and
200 ms to write. A 2-second latency spike is indistinguishable from 2 seconds of
the database being gone. This is deliberate - it is the view of a client with
those deadlines - but the sub-second blips above are latency, not unavailability.

**Best case for reconnection.** The probe opens a fresh connection every sample,
so it never experiences what a pooled application does: existing connections
reset, pool exhaustion, retry storms. A 20-second database outage can present as
a longer application incident.

**Idle database.** Only the probe was writing. Stalls under real write volume
may be longer.

**Attribution depends on spacing.** Outages are attributed to the disruption
whose window they fall in. With `-settle 90s`, repetition 2 started 2-3
minutes after repetition 1 recovered, so repetition 1's later outages were
recorded against repetition 2.

## Open questions

1. Is the 5-minute stall the forced WAL switch? Sample `pg_stat_archiver` every
   second, or set `archive_timeout = 900` and see whether the stalls move.
2. What caused the read-and-write blips at exact wall-clock times on two
   independent instances (20:00:01 on both, 2026-10-02)? Not seen since.
3. Do stalls grow with write volume? Needs a reconnecting load generator;
   pgbench aborts on disconnect.
4. Does a Multi-AZ *reboot* (no failover) behave differently? Never measured.
