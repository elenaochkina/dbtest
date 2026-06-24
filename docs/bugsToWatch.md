# Bugs to Watch

A running list of known latent bugs and correctness risks that are not yet
fixed. Each entry: what it is, where it lives, why it happens, impact, and the
proposed fix. Remove an entry once it's fixed and verified.

---

## 1. A panicking scenario is registered as `passed = true`

**Status:** open — not yet observed in practice, flagged by code inspection.

**Location:**
- Trigger: `scenario/scenario_engine.go` — the deferred teardown in `Run`.
- Write site: `state/correctness.go` — `func (r *Run) End(ctx, passed bool)`.

**Summary**

If a scenario step *panics* (as opposed to returning an error), the run is
recorded in the `runs` table as `passed = true`, even though it crashed. A step
that *returns an error* is recorded correctly as `passed = false`; only a panic
is mis-registered.

**Why it happens**

`Run` records the outcome from its named return value `err`:

```go
func Run(ctx context.Context, name string, rc *RunContext) (err error) {
    ...
    defer func() {
        ... drain cleanups ...
        // err == nil decides the recorded outcome:
        rc.StateRun.End(context.Background(), err == nil)
    }()
    return sc.executeSteps(ctx, rc)
}
```

- A returned error assigns `err` → `err == nil` is `false` → `End(ctx, false)`. ✅
- A **panic** unwinds the stack *without ever assigning* `err`, so `err` keeps
  its zero value `nil`. The deferred function still runs (Go runs defers during
  panic unwinding), computes `err == nil` → `true`, and calls `End(ctx, true)`.
  The row is stamped `passed = true`, then the panic continues propagating and
  the process crashes. ❌

| Failure mode                          | Surfaces in Go as | Recorded         |
| ------------------------------------- | ----------------- | ---------------- |
| A step returns an error               | `error` value     | `passed = false` ✅ |
| The **harness Go code panics**        | `panic()`         | `passed = true`  ❌ |

**Harness panic vs. database crash — do not confuse them**

This bug is *narrow*: it only triggers when the **harness process** (the Go
program `runbenchmark`) panics. It is unrelated to the **database process**
(`postgres`) crashing. Two different processes, two different failure channels:

- **The database crashes** (expected via the `kill-process` step, or unexpected
  mid-run): pgx returns an **`error`** from the next query, the step does
  `return err`, and the outcome is recorded correctly — `passed = false` for an
  unexpected break, `passed = true` for the crash-recovery scenario when the
  data survives WAL recovery (which is the *correct* pass). A database crash
  flows through the normal error path, so it is **not** affected by this bug.

- **The harness Go code panics** (nil deref, out-of-range index, an un-checked
  type assertion, etc.): control unwinds via `panic()`, never assigning the
  named return `err`, so `End(ctx, true)` records `passed = true`. This is the
  bug, and it happens in **any** scenario — `warehouse`, `benchmark`,
  `crash-recovery` — because it depends on the harness panicking, not on which
  scenario is running.

In short: **DB dies → returned error → recorded correctly. Harness panics →
recorded as passed → bug.** The failure channel (`error` vs `panic`) is what
decides, not the scenario.

**Impact**

The `runs` table — the durable record used for run history and pass/fail
tracking — can show a crashed *harness* run as passed. For a harness whose job
is correctness and durability verification, a false "passed" is the worst kind
of wrong: it hides a failure rather than surfacing it.

**Why it is easy to mis-attribute**

`passed = true` after the `kill-process` step is usually *correct* — the
crash-recovery scenario is supposed to pass when data survives the SIGKILL. So a
passed kill run is almost always the test working, not this bug. This bug is
only in play if the logs show an actual `panic:` / stack trace alongside a
`passed = true` row — i.e. the Go program itself crashed, not the database.

**Proposed fix**

Recover in `Run`'s defer, force `passed = false` on panic, and re-panic so the
crash still surfaces:

```go
defer func() {
    ... drain cleanups ...
    passed := err == nil
    if p := recover(); p != nil {
        passed = false
        defer panic(p) // record first, then let the crash propagate
    }
    if endErr := rc.StateRun.End(context.Background(), passed); endErr != nil && rc.Tel != nil {
        rc.Tel.Logger.Error("end run failed", "error", endErr)
    }
}()
```

**Related improvement (separate, optional)**

The `runs` table records *whether* a run failed, not *why* — the error message
is only logged to stderr in `cmd/runbenchmark/main.go`, not persisted. Adding a
`runs.error_message` column and threading `err`'s string into `End` would make
failure reasons queryable.
