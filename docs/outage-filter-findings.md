# Outage filter findings

How recovery runs turn probe outages into `downtime_results` rows, what goes wrong with
short outages, and a proposed fix. For discussion before changing the code.

## How it works today

`downtimeRows` in `temporal/activities/state_db_activities.go`:

1. `longOutages` drops every outage shorter than 1 second, unless it lost commits or never
   ended.
2. Each disruption is matched with the first remaining writable outage after it.
3. Readable downtime comes from a readable outage of at least 1 second that falls inside that
   writable outage; otherwise it is 0.

## What goes wrong

- **A disruption whose outage is under 1 second gets no row.** The run still passes.
- **`readable_downtime_ms = 0` is ambiguous.** It can mean reads never failed, or that the
  read outage was dropped by the filter.
- **Docker runs save no rows at all.** Crash and restart recover in about 0.75 s.

## Evidence

Aurora failover run `recovery-20261028-132852`, 2 repetitions, passed:

| Repetition | Writable | Readable | Probe errors |
|---|---|---|---|
| 1 | 1.104 s | 0 | `{"timeout": 3}` |
| 2 | no row | | |

```sql
SELECT r.workflow_id, r.repetitions, count(d.id) AS saved_rows, r.passed
FROM runs r LEFT JOIN downtime_results d ON d.run_id = r.id
GROUP BY r.id;
```

- Repetition 1 was just over the 1-second limit, so it was kept. Its readable 0 may be a
  shorter read outage that was dropped.
- Repetition 2 is consistent with an outage under 1 second, or with no failed probe at all;
  both produce no row today.
- The probe's raw outage list was only kept in Temporal's workflow history (the input to
  `SaveDowntimeResults`). That history is gone since the Temporal container was recreated, so
  the exact values can't be checked any more.
- Docker crash, from an earlier run's history: one writable outage of 751.7 ms, 2 failures,
  errors `{"57P03": 1, "refused": 1}`, dropped by the filter.

## Proposed rule

Each disruption owns the time from it until the next disruption; the last one owns everything
until the probe stops.

```
disruption 1 ──────────── disruption 2 ──────────── disruption 3 ──────────── probe stops
   window 1                   window 2                   window 3
```

| | Today | Proposed |
|---|---|---|
| Outages considered | only ≥ 1 s or with lost commits | all |
| Matching | next ≥ 1 s outage anywhere after the disruption | first outage in the disruption's window |
| Disruption with no outage | no row | a row with downtime 0 |
| Rows per run | can be fewer than repetitions | always equal to repetitions |
| Readable downtime | ≥ 1 s read outage inside the write outage, else 0 | first read outage in the same window, any length, else 0 |
| Meaning of readable 0 | ambiguous | no read failed |
| Use of the 1-second limit | drops data | only decides which extra outages are logged as warnings |

An outage that never ended is still an error.

## Identifying blips

A blip is a short failure not caused by the disruption. With "first outage in the window", a
disruption that caused no failure followed by a blip later in its window would pick up the
blip.

| Signal | Caused by the disruption | Blip |
|---|---|---|
| Start, measured from the disruption | within a few seconds | any time, often much later |
| Error kind (`probe_errors`) | `refused`, `57P03` (starting up), `57P01` (admin shutdown), connection reset | mostly `timeout` |
| Size | several failed probes in a row | usually 1 failed probe |
| Seen before the first disruption | no | yes: the probe runs for the settle time first, which shows the normal noise level |

Proposal: record `outage_delay_ms` (first failure minus disruption time) on each row, and show
it next to `probe_errors` on the dashboard. A rule that ignores outages starting more than N
seconds after the disruption can come later, once real runs show normal delays per provider.

## Open questions

1. Save a row with downtime 0 when the probe saw no failure, or save no row?
2. Add `outage_delay_ms` now, with the filter fix?
3. Keep the raw probe outages somewhere other than Temporal history, so findings like this one
   can be checked later?
4. Should a run with fewer rows than repetitions still count as passed?
