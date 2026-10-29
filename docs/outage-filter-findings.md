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

## Proposed code change

In `temporal/activities/state_db_activities.go`, `downtimeRows` is rewritten and
`longOutages` and `matchReadable` are removed:

```go
// downtimeRows makes one row per disruption from the first writable and
// readable outages that start before the next disruption. A disruption with
// no outage gets a row with no downtime. It also returns the long outages
// that no row uses.
func downtimeRows(input SaveDowntimeInput) ([]state.DowntimeRow, []probe.Outage, error) {
	if len(input.DisruptedAt) == 0 {
		return nil, nil, fmt.Errorf("no disruption timestamps recorded")
	}
	var rows []state.DowntimeRow
	used := map[time.Time]bool{}
	for index, disruptedAt := range input.DisruptedAt {
		var end time.Time
		if index+1 < len(input.DisruptedAt) {
			end = input.DisruptedAt[index+1]
		}
		row := state.DowntimeRow{
			Provider:        string(input.Provider),
			Disruption:      string(input.Disruption),
			Repetition:      index + 1,
			DisruptedAt:     disruptedAt,
			UsableAgainAt:   disruptedAt,
			ProbeIntervalMs: input.Result.IntervalMs,
		}
		if outage, found := firstOutage(input.Result.Writable.Outages, disruptedAt, end); found {
			if outage.FirstOKAfter.IsZero() {
				return nil, nil, fmt.Errorf("disruption %d never recovered before the probe stopped", index+1)
			}
			used[outage.FirstFailure] = true
			row.UsableAgainAt = outage.FirstOKAfter
			row.WritableDowntimeMs = outage.DownMs
			row.LostCommits = outage.LostCommits
			row.ProbeFailures = outage.Failures
			row.ProbeErrors = outage.Errors
		}
		if outage, found := firstOutage(input.Result.Readable.Outages, disruptedAt, end); found {
			row.ReadableDowntimeMs = outage.DownMs
		}
		rows = append(rows, row)
	}
	var unused []probe.Outage
	for _, outage := range input.Result.Writable.Outages {
		if !used[outage.FirstFailure] && (outage.DownMs >= 1000 || outage.LostCommits > 0) {
			unused = append(unused, outage)
		}
	}
	return rows, unused, nil
}

// firstOutage returns the first outage that starts at or after start and,
// when end is set, before end.
func firstOutage(outages []probe.Outage, start, end time.Time) (probe.Outage, bool) {
	for _, outage := range outages {
		if outage.FirstFailure.Before(start) {
			continue
		}
		if !end.IsZero() && !outage.FirstFailure.Before(end) {
			break
		}
		return outage, true
	}
	return probe.Outage{}, false
}
```

How it works:

1. Each disruption's window runs from its own timestamp to the next disruption's (`end`);
   the last window has no end.
2. `firstOutage` returns the first outage that starts inside the window, of any length.
3. Every disruption gets a row. Without a writable outage, the row keeps downtime 0, no
   failures and `usable_again_at` equal to the disruption time.
4. Readable downtime comes from the first readable outage in the same window, so 0 means no
   read failed.
5. Outages no row used are returned only if they are long (≥ 1 s or lost commits), for the
   "outage matched no disruption" warning.

`SaveDowntimeResults` changes one log line: "disruptions with no outage" counted missing rows,
which no longer happen. It becomes an info line, "disruptions with no failed probe", counting
rows with 0 probe failures.

With `outage_delay_ms` (open question 2), the schema gets one column in `downtime_results`,
`DowntimeRow` one field, and the row sets it from the writable outage:

```go
row.OutageDelayMs = float64(outage.FirstFailure.Sub(disruptedAt).Microseconds()) / 1000
```

Proposed tests, in a new `state_db_activities_test.go`: a 750 ms outage gets a row; a
disruption with no outage gets a row with 0 downtime; readable is 0 when reads never failed;
a blip before the first disruption is returned as unused.

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
