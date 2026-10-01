# RDS Multi-AZ failover: observations

Measurements from three forced failovers on 2026-09-28 and 2026-10-01.

## Setup

- RDS PostgreSQL 16.13, `db.t3.micro`, Multi-AZ, us-west-2, created per run
- Disruption: `RebootDBInstance` with `ForceFailover: true`
- Probe: one Fargate task in the same VPC, fresh connection every 250 ms,
  300 ms connect+read deadline, 200 ms write deadline
- Two availability levels per sample: `SELECT` (readable) then `UPDATE` (writable)
- Seeded with pgbench scale 100; no concurrent load beyond the probe

## Headline numbers

| run | takeover (write) | takeover (read) | read/write gap | to full recovery |
|---|---|---|---|---|
| A, 09-28 | 37.0 s | 32.8 s | 4259 ms | 338 s |
| B, 10-01 | 33.7 s | 33.2 s | 501 ms | 332 s |
| C rep 1 | 35.2 s | 33.7 s | 1505 ms | 35 s (see attribution) |
| C rep 2 | 16.8 s | 16.3 s | 501 ms | 553 s |

For comparison, same harness: RDS single-AZ reboot 13.8 / 10.3 / 7.8 s,
Docker crash 0.45-0.51 s.

**`lost_commits` was 0 in every failover.** Synchronous replication held; no
acknowledged write was lost.

## 1. Writes fail before reads

Writes stop at the instant `RebootDBInstance` is called; reads keep working
until AWS logs "failover started", then both go. Both levels recover on the
same sample.

Run A, the clearest case:

```
21:56:55.793  writes start failing
21:56:59.977  AWS: "Multi-AZ instance failover started"
21:57:00.052  reads start failing          <- 4.26 s later
21:57:32.577  both recover                  (same nanosecond)
```

Confirmed two independent ways: the timestamp gap (4259 ms) and the count of
samples that failed on write only (17 x 250 ms = 4250 ms).

The gap is real in all four measurements but varies 10x (501-4259 ms), so it
is not a stable quantity.

## 2. One failover produces several outages

Every failover was followed by brief interruptions while the standby was
rebuilt. Observed three times, so this is structural rather than incidental.

```
run A   37.0 s takeover,  then 551 ms at +43 s,   1996 ms at +5 m 36 s
run B   33.7 s takeover,  then 1181 ms at +164 s,  615 ms at +298 s
run C   16.8 s takeover,  then 5 x ~551 ms spread over 9 minutes
```

In run B the second blip failed **writes only** - reads were unaffected for its
whole duration, which is the signature of the primary waiting on a standby
acknowledgement.

## 3. AWS's event log is wrong in both directions

`Multi-AZ instance failover completed` consistently postdates the database
being usable, and long predates the interruptions stopping.

```
run A   probe recovered 21:57:32.577,  AWS "completed" 21:57:59.695  (+27 s)
run B   probe recovered 04:27:17.361,  AWS "completed" 04:27:59.122  (+42 s)
run C   probe recovered 05:08:04.362,  AWS "completed" 05:08:40.037  (+36 s)
```

In run C the event log then records **nothing** for the next nine minutes,
during which the probe captured five separate interruptions. There is no event
for "standby rebuilt" or "high availability restored".

So the event log is usable for identifying *what kind* of event occurred - the
`failover` event category is reliable - but not for timing anything.

## 4. The AvailabilityZone field swaps late, by an unpredictable amount

`DescribeDBInstances.AvailabilityZone` does eventually report the new primary,
but the delay after "failover completed" measured:

```
run A         ~5.5 minutes
run C rep 1   ~1 minute
run C rep 2   6-9 minutes   (bounded; not observed precisely)
```

No event marks the swap, so it is observable only by polling. It is a sound
*correctness* check - it always eventually swaps, which proves a failover
occurred - but a poor readiness signal.

## 5. Periodic ~551 ms write stalls while the pair is degraded (new, one run)

Run C repetition 2 produced five interruptions, all identical:

```
05:10:00.148   551.635 ms   1 failed sample   timeout
05:11:00.075   551.366 ms   1 failed sample   timeout
05:11:09.897   551.183 ms   1 failed sample   timeout
05:14:59.991   551.391 ms   1 failed sample   timeout
05:17:00.062   552.224 ms   1 failed sample   timeout
```

Four of five land within 150 ms of a minute boundary. All are a single sample
exceeding the 200 ms write deadline - the server answered, slowly.

They occur **only** between the failover and the AZ swap: none in the 98-second
baseline, none in the three minutes between repetition 1's recovery and
repetition 2's disruption, and none after 05:17:00. Repetition 1, whose AZ swap
took about a minute, produced none at all.

**Inference, not established:** the periodicity suggests a scheduled task on a
60-second timer that stalls commits while the deployment lacks a healthy
standby, rather than a continuous WAL catch-up. Mechanism unknown.

**Control experiment that would settle it:** provision with `-ha`, start the
probe, apply no disruption, sample for ten minutes. If ~551 ms blips appear once
a minute anyway, they are a property of a `db.t3.micro` with a standby and
unrelated to failover.

## Measurement caveats

**"Outage" includes "slow".** The probe allows 300 ms to connect and read and
200 ms to write. A 2-second latency spike is indistinguishable from 2 seconds of
the database being gone. This is deliberate - it is the view of a client with
those deadlines - but the sub-second blips above are latency, not unavailability.

**Best case for reconnection.** The probe opens a fresh connection every sample,
so it never experiences what a pooled application does: existing connections
reset, pool exhaustion, retry storms. A 37-second database outage can present as
a longer application incident.

**Idle database.** Only the probe was writing. The sync phase under real write
volume would have more WAL to reconcile, so the stall durations here are a floor.

**Attribution depends on spacing.** Outages are attributed to the disruption
whose window they fall in. Run C repetition 1 shows no tail because the next
disruption began 3.5 minutes later, shorter than the 5-9 minute tail - so
repetition 1's late blips were recorded against repetition 2. Per-repetition
`full_recovery_ms` is only meaningful when disruptions are spaced wider than the
tail.

## Open questions

1. Are the per-minute blips failover-specific, or always present on a Multi-AZ
   `db.t3.micro`? (control experiment above)
2. How much of the ~35 s takeover is the server versus the endpoint? The RDS
   PostgreSQL error log records `database system is ready to accept connections`;
   comparing that to the probe's first success would split server readiness from
   client reachability. The instance is deleted at run end, so the log must be
   fetched before deprovision.
3. Does the tail scale with write volume? Needs a reconnecting load generator;
   pgbench aborts on disconnect and cannot be used.
4. Does a Multi-AZ *reboot* (no failover) have a tail? Never measured. The
   standby misses WAL while the primary restarts.
