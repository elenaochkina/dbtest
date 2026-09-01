package probe

import (
	"context"
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
	MaxDuration  time.Duration // backstop if nothing ever stops the probe
}

// Probe measures database availability at two levels and checks that
// acknowledged commits survive a disruption.
type Probe struct {
	cfg Config

	readable availabilityRecorder // connect + read the counter
	writable availabilityRecorder // counter advanced and acked

	// seq is the highest counter value the server has returned.
	seq int64
}

func New(cfg Config) *Probe {
	return &Probe{cfg: cfg}
}

// Prepare creates the counter row the writable observation advances.
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

			if _, err := conn.Exec(cctx, `CREATE TABLE IF NOT EXISTS dbtest_probe (
				id  INT PRIMARY KEY,
				seq BIGINT NOT NULL,
				ts  TIMESTAMPTZ NOT NULL DEFAULT now()
			)`); err != nil {
				return err
			}
			_, err = conn.Exec(cctx,
				"INSERT INTO dbtest_probe (id, seq) VALUES (1, 0) ON CONFLICT (id) DO NOTHING")
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

// Run polls until it is stopped or the deadline passes. How many disruptions to
// watch for is the caller's business, not the probe's.
func (p *Probe) Run(ctx context.Context) Result {
	res := Result{
		StartedAt:  time.Now(),
		IntervalMs: float64(p.cfg.Interval.Microseconds()) / 1000,
	}
	deadline := res.StartedAt.Add(p.cfg.MaxDuration)

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		started := time.Now()
		lost, readErr, writeErr := p.sample(ctx)

		// A sample cut short by shutdown says nothing about the database.
		if ctx.Err() != nil {
			break
		}
		res.Samples++

		// Attributed while the outage is still open, since it closes later.
		if lost > 0 {
			slog.Error("acknowledged commits lost", "count", lost)
			p.writable.recordLostCommits(lost)
			res.LostCommits += lost
		}

		p.readable.record(started, readErr)
		// Writable recovers last, so it brackets the outage that matters.
		if closed := p.writable.record(started, writeErr); closed != nil {
			slog.Info("outage ended",
				"down_ms", closed.DownMs,
				"failures", closed.Failures,
				"lost_commits", closed.LostCommits,
				"observed", len(p.writable.completedOutages),
			)
		}

		if rest := p.cfg.Interval - time.Since(started); rest > 0 {
			time.Sleep(rest)
		}
	}

	res.Readable = p.readable.availability()
	res.Writable = p.writable.availability()
	res.EndedAt = time.Now()
	res.AckedCommits = p.seq
	return res
}

// sample takes both observations from one connection, so they describe the same
// server at the same moment. The connection is new every time: a held one can
// keep working after the server would refuse a new one.
// It also reports how many acknowledged commits the server has lost.
func (p *Probe) sample(ctx context.Context) (lost int64, readErr, writeErr error) {
	cctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	conn, err := pgx.Connect(cctx, p.cfg.DSN)
	if err != nil {
		return 0, err, err
	}
	defer conn.Close(context.Background())

	var stored int64
	if err := conn.QueryRow(cctx, "SELECT seq FROM dbtest_probe WHERE id = 1").Scan(&stored); err != nil {
		return 0, err, err
	}

	// The write gets a shorter timeout than the connect: a hanging write would
	// stretch the whole sample.
	wctx, wcancel := context.WithTimeout(ctx, p.cfg.WriteTimeout)
	defer wcancel()
	var seq int64
	if err := conn.QueryRow(wctx,
		"UPDATE dbtest_probe SET seq = seq + 1, ts = now() WHERE id = 1 RETURNING seq").Scan(&seq); err != nil {
		return 0, nil, err
	}

	// If the newly returned seq is less than or equal to the highest one
	// already returned, one or more acknowledged commits were lost.
	if seq <= p.seq {
		lost = p.seq - seq + 1
	}
	p.seq = seq
	return lost, nil, nil
}
