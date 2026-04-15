package adapter_test

import (
	"context"
	"os"
	"testing"

	"github.com/elenaochkina/dbtest/adapter"
)

func TestConnect(t *testing.T) {
	dsn := os.Getenv("DSN")
	if dsn == "" {
		t.Skip("DSN not set — skipping integration test")
	}

	pool, err := adapter.Connect(dsn)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	ctx := context.Background()

	// Create a temporary table, scoped to this connection session.
	_, err = pool.Exec(ctx, `
		CREATE TEMP TABLE test_rows (
			id   SERIAL PRIMARY KEY,
			name TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create temp table: %v", err)
	}

	// Insert 3 rows.
	rows := []string{"alice", "bob", "carol"}
	for _, name := range rows {
		_, err = pool.Exec(ctx, `INSERT INTO test_rows (name) VALUES ($1)`, name)
		if err != nil {
			t.Fatalf("insert %q: %v", name, err)
		}
	}

	// Read back and assert count.
	var count int
	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM test_rows`).Scan(&count)
	if err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != len(rows) {
		t.Errorf("expected %d rows, got %d", len(rows), count)
	}
}
