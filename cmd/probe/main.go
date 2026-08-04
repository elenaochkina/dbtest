// Command probe measures database availability at two levels and checks that
// acknowledged commits survive a disruption.
//
// Each sample opens one connection and takes both observations from it:
// "reachable" (the server answered a trivial query) and "writable" (the server
// acked an insert).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/elenaochkina/dbtest/probe"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// detector watches one condition and brackets the outages in it. Working state
// only — an outage is the gap between two observations, so deciding what a
// failure means needs to remember what came before.
type detector struct {
	recoveredAfter int
	seenFirstOK    bool
	lastOK         time.Time
	current        *probe.Outage
	consecutiveOK  int
	outages        []probe.Outage
	completed      int // outages that closed properly
}

// record feeds one observation in.
// It returns the outage that just completed, or nil.
func (d *detector) record(at time.Time, err error) *probe.Outage {
	if err == nil {
		d.lastOK = at
		// Until the condition has held once, "not holding" is indistinguishable
		// from "never established", so no outage can be opened.
		if !d.seenFirstOK {
			d.seenFirstOK = true
			return nil
		}
		if d.current == nil {
			return nil
		}

		d.consecutiveOK++
		// Stamped at the first success, not at the one that confirms it — the
		// database was back at the earlier moment.
		if d.current.FirstOKAfter.IsZero() {
			d.current.FirstOKAfter = at
			d.current.DownMs = float64(at.Sub(d.current.LastOK).Microseconds()) / 1000
		}
		// Wait for a run of successes before believing it: a database
		// mid-recovery can accept one connection and refuse the next.
		if d.consecutiveOK < d.recoveredAfter {
			return nil
		}

		d.outages = append(d.outages, *d.current)
		d.current = nil
		d.completed++
		return &d.outages[len(d.outages)-1]
	}

	d.consecutiveOK = 0
	if !d.seenFirstOK {
		return nil
	}
	if d.current == nil {
		d.current = &probe.Outage{LastOK: d.lastOK, FirstFailure: at, Errors: map[string]int{}}
	}
	// A success can be followed by more failures — reopen the outage and let the
	// run-of-successes rule decide when it really ended.
	d.current.FirstOKAfter = time.Time{}
	d.current.DownMs = 0
	d.current.Failures++
	d.current.Errors[classify(err)]++
	return nil
}

// report returns what the detector saw, including an outage that is still open.
func (d *detector) report() probe.Availability {
	a := probe.Availability{
		Outages:   append([]probe.Outage(nil), d.outages...),
		Recovered: d.completed > 0,
	}
	if d.current != nil {
		a.Outages = append(a.Outages, *d.current)
	}
	for _, o := range a.Outages {
		if o.DownMs > a.LongestDownMs {
			a.LongestDownMs = o.DownMs
		}
	}
	return a
}

func main() {
	dsn := flag.String("dsn", "", "target database DSN (required)")
	interval := flag.Duration("interval", 20*time.Millisecond, "time between samples")
	timeout := flag.Duration("timeout", 2*time.Second, "per-sample connect + read timeout")
	writeTimeout := flag.Duration("write-timeout", 500*time.Millisecond, "per-sample write timeout")
	maxDuration := flag.Duration("max-duration", 10*time.Minute, "give up if the expected outages never arrive")
	recoveredAfter := flag.Int("recovered-after", 20, "consecutive successes that end an outage")
	repetitions := flag.Int("repetitions", 1, "how many disruptions to observe before exiting")
	flag.Parse()

	if *dsn == "" {
		slog.Error("-dsn is required")
		os.Exit(2)
	}
	if *repetitions < 1 {
		slog.Error("-repetitions must be at least 1")
		os.Exit(2)
	}

	// SIGTERM is how a container is asked to stop;
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := &prober{
		dsn:          *dsn,
		interval:     *interval,
		timeout:      *timeout,
		writeTimeout: *writeTimeout,
		repetitions:  *repetitions,
		reachable:    detector{recoveredAfter: *recoveredAfter},
		writable:     detector{recoveredAfter: *recoveredAfter},
	}

	if err := p.prepare(ctx); err != nil {
		slog.Error("prepare probe table", "error", err)
		os.Exit(1)
	}

	res := p.run(ctx, *maxDuration)

	payload, err := json.Marshal(res)
	if err != nil {
		slog.Error("marshal result", "error", err)
		os.Exit(1)
	}
	fmt.Println(string(payload))

	// A measurement that never saw what it was sent to see is a failure of the
	// prober's job. Lost commits are not — that is a successful measurement of a
	// bad outcome, and the caller decides what it means.
	if res.Error != "" {
		slog.Error("measurement incomplete", "error", res.Error)
		os.Exit(1)
	}
}

type prober struct {
	dsn          string
	interval     time.Duration
	timeout      time.Duration
	writeTimeout time.Duration
	repetitions  int

	reachable detector // connect + SELECT 1
	writable  detector // INSERT acked

	// seq is handed to the next insert and never reused
	seq int64
	// watermark is the highest seq the server explicitly acked;
	watermark int64
	acked     int64
}

// prepare creates the table the writable observation inserts into.
// Retried in case prober has been started but the database is still in booting mode.
func (p *prober) prepare(ctx context.Context) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err := func() error {
			cctx, cancel := context.WithTimeout(ctx, p.timeout)
			defer cancel()

			conn, err := pgx.Connect(cctx, p.dsn)
			if err != nil {
				return err
			}
			defer conn.Close(context.Background())

			_, err = conn.Exec(cctx, `CREATE TABLE IF NOT EXISTS dbtest_probe (
				seq BIGINT PRIMARY KEY,
				ts  TIMESTAMPTZ NOT NULL DEFAULT now()
			)`)
			return err
		}()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return err
		}
		slog.Warn("database not ready for setup yet", "error", err)
		time.Sleep(time.Second)
	}
}

// run polls until it has watched the expected number of outages end, the
// deadline passes, or the context is cancelled.
func (p *prober) run(ctx context.Context, maxDuration time.Duration) probe.Result {
	res := probe.Result{
		StartedAt:   time.Now(),
		IntervalMs:  float64(p.interval.Microseconds()) / 1000,
		Repetitions: p.repetitions,
	}
	deadline := res.StartedAt.Add(maxDuration)

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		started := time.Now()
		reachErr, writeErr := p.sample(ctx)
		res.Samples++

		p.reachable.record(started, reachErr)
		// Writable recovers last, so it decides when the run is done.
		if closed := p.writable.record(started, writeErr); closed != nil {
			lost, err := p.lostCommits(ctx)
			if err != nil {
				slog.Error("durability check failed", "error", err)
			} else {
				closed.LostCommits = lost
				res.LostCommits += lost
			}
			slog.Info("outage ended",
				"down_ms", closed.DownMs,
				"failures", closed.Failures,
				"lost_commits", closed.LostCommits,
				"observed", p.writable.completed,
				"expected", p.repetitions,
			)
			if p.writable.completed >= p.repetitions {
				break
			}
		}

		if rest := p.interval - time.Since(started); rest > 0 {
			time.Sleep(rest)
		}
	}

	res.Reachable = p.reachable.report()
	res.Writable = p.writable.report()
	res.EndedAt = time.Now()
	res.AckedCommits = p.acked
	if p.writable.completed < p.repetitions {
		res.Error = fmt.Sprintf("observed %d completed outages, expected %d",
			p.writable.completed, p.repetitions)
	}
	return res
}

// sample takes both observations from one connection, so they describe the same
// server at the same moment. The connection is new every time: a held one can
// keep working after the server would refuse a new one.
func (p *prober) sample(ctx context.Context) (reachErr, writeErr error) {
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	conn, err := pgx.Connect(cctx, p.dsn)
	if err != nil {
		return err, err
	}
	defer conn.Close(context.Background())

	var one int
	if err := conn.QueryRow(cctx, "SELECT 1").Scan(&one); err != nil {
		return err, err
	}

	// The write gets a shorter timeout than the connect: a hanging write would
	// stretch the whole sample.
	p.seq++
	wctx, wcancel := context.WithTimeout(ctx, p.writeTimeout)
	defer wcancel()
	if _, err := conn.Exec(wctx, "INSERT INTO dbtest_probe (seq) VALUES ($1)", p.seq); err != nil {
		return nil, err
	}

	p.watermark = p.seq
	p.acked++
	return nil, nil
}

// lostCommits counts rows the server acked and then lost. Extra rows from
// indeterminate writes are ignored.
func (p *prober) lostCommits(ctx context.Context) (int64, error) {
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	conn, err := pgx.Connect(cctx, p.dsn)
	if err != nil {
		return 0, err
	}
	defer conn.Close(context.Background())

	var present int64
	if err := conn.QueryRow(cctx,
		"SELECT count(*) FROM dbtest_probe WHERE seq <= $1", p.watermark).Scan(&present); err != nil {
		return 0, err
	}
	if lost := p.acked - present; lost > 0 {
		return lost, nil
	}
	return 0, nil
}

// classify buckets an error by recovery phase
func classify(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "reset"
	default:
		return "other"
	}
}
