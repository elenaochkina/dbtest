package state

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SaveFingerprint records a content-hash digest of a table at a named point in
// the run (e.g. "before_restart"). Both the snapshot and verify steps call this,
// so the run carries a durable audit trail of what the data looked like on each
// side of a control-plane operation.
func (r *Run) SaveFingerprint(ctx context.Context, name, table, digest string) error {
	_, err := r.Pool.Exec(ctx,
		`INSERT INTO fingerprints (run_id, name, table_name, digest, captured_at)
		 VALUES ($1, $2, $3, $4, now())`,
		r.ID, name, table, digest,
	)
	if err != nil {
		return fmt.Errorf("SaveFingerprint %q/%q: %w", name, table, err)
	}
	r.Logger.Info("fingerprint saved", "run_id", r.ID, "name", name, "table", table, "digest", digest)
	return nil
}

// GetFingerprint reads back a previously saved digest for this run — the
// verify step uses it to fetch the baseline captured by snapshot, so the
// comparison is grounded in persisted state, not an in-memory value.
func (r *Run) GetFingerprint(ctx context.Context, name, table string) (string, error) {
	var digest string
	err := r.Pool.QueryRow(ctx,
		`SELECT digest
		 FROM fingerprints
		 WHERE run_id = $1 AND name = $2 AND table_name = $3`,
		r.ID, name, table,
	).Scan(&digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("fingerprint %q/%q not found for run %s", name, table, r.ID)
	}
	if err != nil {
		return "", fmt.Errorf("GetFingerprint %q/%q: %w", name, table, err)
	}
	return digest, nil
}
