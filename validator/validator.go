package validator

import (
	"context"
	"fmt"
	"log/slog"
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
		tel.ValidatorChecksumDuration.Observe(duration)
		slog.Info("checksum computed", "table", table, "latency_seconds", duration)
	}
	return c, nil
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
