package probe

import (
	"context"
	"errors"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// availabilityRecorder brackets the outages in one condition. An outage is the
// gap between two observations, so the recorder remembers what came before.
type availabilityRecorder struct {
	lastOK           time.Time //holds the time of the last successful check
	currentOutage    *Outage
	completedOutages []Outage
}

// record feeds one observation in.
// It returns the outage that just completed, or nil.
func (r *availabilityRecorder) record(at time.Time, err error) *Outage {
	if err != nil {
		r.down(at, err)
		return nil
	}
	return r.up(at)
}

// down records a failed observation, opening an outage if none is open.
func (r *availabilityRecorder) down(at time.Time, err error) {
	//No outage is recorded until the first success.
	if r.lastOK.IsZero() {
		return
	}
	if r.currentOutage == nil {
		r.currentOutage = &Outage{LastOK: r.lastOK, FirstFailure: at, Errors: map[string]int{}}
	}
	r.currentOutage.fail(err)
}

// up records a successful observation and closes the open outage once it has
// held. It returns the outage that just completed, or nil.
func (r *availabilityRecorder) up(at time.Time) *Outage {
	r.lastOK = at
	if r.currentOutage == nil {
		return nil
	}
	if !r.currentOutage.succeed(at) {
		return nil
	}

	r.completedOutages = append(r.completedOutages, *r.currentOutage)
	r.currentOutage = nil
	return &r.completedOutages[len(r.completedOutages)-1]
}

// recordLostCommits attributes lost commits to the open outage.
func (r *availabilityRecorder) recordLostCommits(n int64) {
	if r.currentOutage != nil {
		r.currentOutage.LostCommits += n
	}
}

// availability returns what the recorder saw, including an outage that is still
// open.
func (r *availabilityRecorder) availability() Availability {
	availability := Availability{
		Outages:   append([]Outage(nil), r.completedOutages...),
		Recovered: len(r.completedOutages) > 0,
	}
	if r.currentOutage != nil {
		availability.Outages = append(availability.Outages, *r.currentOutage)
	}
	for _, outage := range availability.Outages {
		if outage.DownMs > availability.LongestDownMs {
			availability.LongestDownMs = outage.DownMs
		}
	}
	return availability
}

// fail records one failed observation against the outage and discards any
// recovery in progress. A success can be followed by more failures.
func (o *Outage) fail(err error) {
	o.consecutiveOK = 0
	o.FirstOKAfter = time.Time{}
	o.DownMs = 0
	o.Failures++
	o.Errors[classify(err)]++
}

// succeed records one successful observation against the outage and reports
// whether it has held long enough to be closed.
func (o *Outage) succeed(at time.Time) bool {
	o.consecutiveOK++
	// Stamped at the first success, not at the one that confirms it. The database
	// was back at the earlier moment.
	if o.FirstOKAfter.IsZero() {
		o.FirstOKAfter = at
		o.DownMs = float64(at.Sub(o.LastOK).Microseconds()) / 1000
	}
	// A database mid-recovery can accept one connection and refuse the next, and a
	// managed failover can flap for seconds. 20 samples is 5s of stability at the
	// default interval.
	return o.consecutiveOK >= 20
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
