package hammerdb

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// RoundTrip times 1000 SELECT 1 queries on one connection and returns their
// p50 and p95 in milliseconds.
func RoundTrip(ctx context.Context, dsn string) (map[string]float64, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)

	times := make([]time.Duration, 1000)
	for i := range times {
		start := time.Now()
		if _, err := conn.Exec(ctx, "SELECT 1"); err != nil {
			return nil, err
		}
		times[i] = time.Since(start)
	}
	slices.Sort(times)
	milliseconds := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	return map[string]float64{
		"select1_p50_ms": milliseconds(times[len(times)/2]),
		"select1_p95_ms": milliseconds(times[len(times)*95/100]),
	}, nil
}
