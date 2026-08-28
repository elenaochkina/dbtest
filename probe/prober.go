package probe

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// Config is everything the probe needs to run.
type Config struct {
	DSN          string
	Interval     time.Duration // time between samples
	Timeout      time.Duration // per-sample connect + read timeout
	WriteTimeout time.Duration // per-sample write timeout
	MaxDuration  time.Duration // give up if the expected outages never arrive
	Repetitions  int           // disruptions to observe before returning
}

// Probe measures database availability at two levels and checks that
// acknowledged commits survive a disruption.
type Probe struct {
	cfg Config

	reachable availabilityRecorder // connect + SELECT 1
	writable  availabilityRecorder // INSERT acked

	// seq is handed to the next insert and never reused
	seq int64
	// watermark is the highest seq the server explicitly acked;
	watermark int64
	acked     int64
}

func New(cfg Config) *Probe {
	return &Probe{cfg: cfg}
}

// Prepare creates the table the writable observation inserts into.
// Retried in case the probe has been started but the database is still in
// booting mode.
func (p *Probe) Prepare(ctx context.Context) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err := func() error {
			cctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
			defer cancel()

			conn, err := pgx.Connect(cctx, p.cfg.DSN)
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

// Run polls until it has watched the expected number of outages end, the
// deadline passes, or the context is cancelled.
func (p *Probe) Run(ctx context.Context) Result {
	res := Result{
		StartedAt:   time.Now(),
		IntervalMs:  float64(p.cfg.Interval.Microseconds()) / 1000,
		Repetitions: p.cfg.Repetitions,
	}
	deadline := res.StartedAt.Add(p.cfg.MaxDuration)

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
				"observed", len(p.writable.completedOutages),
				"expected", p.cfg.Repetitions,
			)
			if len(p.writable.completedOutages) >= p.cfg.Repetitions {
				break
			}
		}

		if rest := p.cfg.Interval - time.Since(started); rest > 0 {
			time.Sleep(rest)
		}
	}

	res.Reachable = p.reachable.availability()
	res.Writable = p.writable.availability()
	res.EndedAt = time.Now()
	res.AckedCommits = p.acked
	if len(p.writable.completedOutages) < p.cfg.Repetitions {
		res.Error = fmt.Sprintf("observed %d completed outages, expected %d",
			len(p.writable.completedOutages), p.cfg.Repetitions)
	}
	return res
}

// sample takes both observations from one connection, so they describe the same
// server at the same moment. The connection is new every time: a held one can
// keep working after the server would refuse a new one.
func (p *Probe) sample(ctx context.Context) (reachErr, writeErr error) {
	cctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	conn, err := pgx.Connect(cctx, p.cfg.DSN)
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
	wctx, wcancel := context.WithTimeout(ctx, p.cfg.WriteTimeout)
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
func (p *Probe) lostCommits(ctx context.Context) (int64, error) {
	cctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	conn, err := pgx.Connect(cctx, p.cfg.DSN)
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
