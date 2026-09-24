# Running RecoveryWorkflow on Docker

All commands verified 2026-09-22 on branch `disruption-api`.

## Build

```bash
cd ~/code/dbtest
make images                      # bench + probe containers; they embed cmd/bench and cmd/probe
go build -o worker ./cmd/worker
go build -o starter ./cmd/starter
```

Rebuild the images after touching `cmd/bench`, `cmd/probe`, or anything they import.
A stale image runs old code without saying so.

## Services

Three terminals, each in the foreground.

**Terminal 1 — Temporal**

```bash
temporal server start-dev --port 7233 --ui-port 8233
```

Dashboard: http://localhost:8233

**Terminal 2 — worker**

```bash
cd ~/code/dbtest
docker start dbtest-state        # state Postgres, host port 5433, password 'test'
STATE_DSN='postgres://postgres:test@localhost:5433/postgres' ./worker
```

Wait for `worker started task_queue=dbtest-tq`.

**Terminal 3 — runs**

## Run

```bash
./starter -workflow recovery -provider docker \
  -disruption crash -repetitions 3 -settle 8s \
  -scale 100 -probe-interval 20ms -id recovery-crash-s100
```

`-workflow` defaults to `pgbench`, so `recovery` is required. Give each run a
distinct `-id`; reusing one while the previous is still running conflicts.

## Results

```bash
PGPASSWORD=test psql -h localhost -p 5433 -U postgres -d postgres -c "
SELECT disruption, repetition, readable_downtime_ms, writable_downtime_ms,
       lost_commits, probe_interval_ms, probe_failures
FROM downtime_results
WHERE run_id = (SELECT run_id FROM downtime_results ORDER BY created_at DESC LIMIT 1)
ORDER BY repetition;"
```

## Postgres logs

`Deprovision` removes the database container, so capture during the run. Start
this once the container appears; the snapshot loop survives the restarts that
kill `docker logs -f`.

```bash
C=$(docker ps --filter name=dbtest --format '{{.Names}}' | grep -v 'dbtest-state\|probe\|seed' | head -1)

while docker inspect $C >/dev/null 2>&1; do
  docker logs --timestamps $C > /tmp/pg.tmp 2>&1 && mv /tmp/pg.tmp /tmp/pg.log
  sleep 1
done &
```

```bash
grep -E "shutdown|redo|ready to accept|checkpoint (starting|complete)|not properly" /tmp/pg.log
```

`redo starts at` / `redo done at` appear only for `crash`. `restart` logs
`database system was shut down at` instead and replays nothing.

## Cleanup

```bash
docker ps -a --filter name=dbtest --format '{{.Names}}\t{{.Status}}'   # expect only dbtest-state
docker volume ls -q | wc -l                                            # expect 1
docker system df | grep -i "local volumes"                             # expect 1  1  ...  0B (0%)
```

Ctrl-C each terminal. If the worker was backgrounded, `kill` it by PID —
`kill %1` only works in the shell that started it.

## Flags

| flag | default | notes |
|---|---|---|
| `-workflow` | `pgbench` | must be `recovery` |
| `-disruption` | `restart` | `crash`, `restart`, `failover` (refused on Docker) |
| `-repetitions` | 3 | not validated; `0` produces no rows |
| `-settle` | 30s | wait after each disruption |
| `-scale` | 1 | pgbench scale; 100 = 10M rows, 1503 MB |
| `-probe-interval` | prober default 250ms | use 20ms on Docker; 250ms is the cloud size |
| `-memory-mib` | 2048 | real cgroup cap; page cache counts against it |
| `-id` | workflow name | unique per concurrent run |

## Gotchas

A port check is not a Temporal readiness check. The worker may fail its first
dial. Use:

```bash
until temporal operator cluster health 2>/dev/null | grep -q SERVING; do sleep 1; done
```

`-probe-interval 250ms` against a ~300ms Docker restart is at the resolution
floor and rounds outages up by up to one full interval. Use 20ms locally.

Repetition 1 carries the seed's un-checkpointed WAL and runs long on `crash`
(2.1s and 2.9s across two identical runs, against ~300ms for repetitions 2 and
3). The magnitude is an accident of where the last checkpoint landed during
seeding, not a property of the database.

Killing the worker mid-run skips `defer Deprovision` and strands the database
container and its volume.
