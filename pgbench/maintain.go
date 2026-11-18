package pgbench

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Scale returns the scale factor of the pgbench tables at dsn: the row count
// of pgbench_branches.
func Scale(ctx context.Context, dsn string) (int, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return 0, err
	}
	defer conn.Close(ctx)

	var scale int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pgbench_branches").Scan(&scale); err != nil {
		return 0, err
	}
	if scale == 0 {
		return 0, errors.New("pgbench_branches is empty")
	}
	return scale, nil
}

// Maintain empties pgbench_history, then runs VACUUM ANALYZE on the pgbench
// tables and CHECKPOINT. It reports whether the checkpoint ran: a role
// without the right to checkpoint skips it.
func Maintain(ctx context.Context, dsn string) (bool, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return false, err
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "TRUNCATE pgbench_history"); err != nil {
		return false, err
	}
	if _, err := conn.Exec(ctx, "VACUUM ANALYZE pgbench_accounts, pgbench_branches, pgbench_tellers, pgbench_history"); err != nil {
		return false, err
	}
	_, err = conn.Exec(ctx, "CHECKPOINT")
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" {
		return false, nil
	}
	return err == nil, err
}
