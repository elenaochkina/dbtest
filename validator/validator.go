package validator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/elenaochkina/dbtest/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Checksum is a point-in-time snapshot of a table's row count and total stock.
type Checksum struct {
	RowCount int64
	StockSum int64
}

// Metrics is the domain-neutral observability view over a Checksum, satisfying
// workload.Result so the warehouse workload can report numbers, not just
// pass/fail. Typed callers keep using RowCount / StockSum directly.
func (c Checksum) Metrics() map[string]float64 {
	return map[string]float64{
		"row_count": float64(c.RowCount),
		"stock_sum": float64(c.StockSum),
	}
}

// ComputeChecksum reads COUNT(*) and SUM(stock_count) from the named table.
// The table name is provided as a string because pgx does not support parameterised
// identifiers — only use this with trusted, hardcoded table names in tests.
func ComputeChecksum(ctx context.Context, db *pgxpool.Pool, table string, tel *telemetry.Telemetry) (Checksum, error) {
	start := time.Now()
	var c Checksum
	// #nosec G201 — table name comes from test code, not user input
	query := fmt.Sprintf(`SELECT COUNT(*), COALESCE(SUM(stock_count), 0) FROM %s`, table)
	err := db.QueryRow(ctx, query).Scan(&c.RowCount, &c.StockSum)
	if err != nil {
		return Checksum{}, fmt.Errorf("checksum %s: %w", table, err)
	}
	duration := time.Since(start).Seconds()
	if tel != nil {
		tel.Metrics.ChecksumDuration.Observe(duration)
		tel.Logger.With("package", "validator").Info("checksum computed", "table", table, "latency_seconds", duration)
	}
	return c, nil
}

// Fingerprint returns a content hash over every row of the named table, in
// deterministic order.
// Like ComputeChecksum, the table name is interpolated rather than
// parameterised (pgx has no identifier parameters), so only call this with
// trusted, hardcoded table names.
func Fingerprint(ctx context.Context, db *pgxpool.Pool, table string, tel *telemetry.Telemetry) (string, error) {
	start := time.Now()
	// #nosec G201 — table name comes from trusted code, not user input
	query := fmt.Sprintf(`SELECT COALESCE(md5(string_agg(t::text, ',' ORDER BY t::text)), '') FROM %s t`, table)
	var digest string
	if err := db.QueryRow(ctx, query).Scan(&digest); err != nil {
		return "", fmt.Errorf("fingerprint %s: %w", table, err)
	}
	if tel != nil {
		tel.Metrics.ChecksumDuration.Observe(time.Since(start).Seconds())
		tel.Logger.With("package", "validator").Info("fingerprint computed",
			"table", table, "digest", digest, "latency_seconds", time.Since(start).Seconds())
	}
	return digest, nil
}

// AssertDelta checks two invariants after an operation:
//  1. RowCount must not change.
//  2. StockSum must have shifted by exactly expectedStockDelta.
//
// Pass a negative delta when stock is expected to decrease (e.g. -10).
func AssertDelta(t *testing.T, before, after Checksum, expectedStockDelta int64) {
	t.Helper()
	if after.RowCount != before.RowCount {
		t.Errorf("row count changed: before=%d after=%d", before.RowCount, after.RowCount)
	}
	actual := after.StockSum - before.StockSum
	if actual != expectedStockDelta {
		t.Errorf("stock delta: expected %d, got %d (before=%d after=%d)",
			expectedStockDelta, actual, before.StockSum, after.StockSum)
	}
}
