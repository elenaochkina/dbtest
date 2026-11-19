package hammerdb

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Baseline is the order and history state of a built schema: each district's
// next order ID and the newest history row.
type Baseline struct {
	warehouses  []int32
	districts   []int32
	nextOrders  []int32
	lastHistory time.Time
}

// Snapshot reads the Baseline of the tpcc database.
func Snapshot(ctx context.Context, dsn string) (Baseline, error) {
	conn, err := connectTPCC(ctx, dsn)
	if err != nil {
		return Baseline{}, err
	}
	defer conn.Close(ctx)

	var base Baseline
	rows, err := conn.Query(ctx, `SELECT d_w_id, d_id, d_next_o_id FROM district`)
	if err != nil {
		return Baseline{}, err
	}
	var warehouse, district, next int32
	_, err = pgx.ForEachRow(rows, []any{&warehouse, &district, &next}, func() error {
		base.warehouses = append(base.warehouses, warehouse)
		base.districts = append(base.districts, district)
		base.nextOrders = append(base.nextOrders, next)
		return nil
	})
	if err != nil {
		return Baseline{}, err
	}
	if len(base.districts) == 0 {
		return Baseline{}, errors.New("tpcc has no districts")
	}
	var last *time.Time
	if err := conn.QueryRow(ctx, `SELECT max(h_date) FROM history`).Scan(&last); err != nil {
		return Baseline{}, err
	}
	if last == nil {
		return Baseline{}, errors.New("tpcc has no history rows")
	}
	base.lastHistory = *last
	return base, nil
}

// Maintain deletes the orders, order lines, new orders and history rows added
// since base, resets each district's next order ID, then runs VACUUM ANALYZE
// and CHECKPOINT. It reports whether the checkpoint ran: a role without the
// right to checkpoint skips it.
func Maintain(ctx context.Context, dsn string, base Baseline) (bool, error) {
	conn, err := connectTPCC(ctx, dsn)
	if err != nil {
		return false, err
	}
	defer conn.Close(ctx)

	trim := `WITH base AS (SELECT * FROM unnest($1::int[], $2::int[], $3::int[]) AS b(w, d, next_o)) `
	statements := []string{
		trim + `DELETE FROM order_line USING base WHERE ol_w_id = w AND ol_d_id = d AND ol_o_id >= next_o`,
		trim + `DELETE FROM new_order USING base WHERE no_w_id = w AND no_d_id = d AND no_o_id >= next_o`,
		trim + `DELETE FROM orders USING base WHERE o_w_id = w AND o_d_id = d AND o_id >= next_o`,
		trim + `UPDATE district SET d_next_o_id = next_o FROM base WHERE d_w_id = w AND d_id = d AND d_next_o_id <> next_o`,
	}
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		for _, statement := range statements {
			if _, err := tx.Exec(ctx, statement, base.warehouses, base.districts, base.nextOrders); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM history WHERE h_date > $1`, base.lastHistory)
		return err
	})
	if err != nil {
		return false, err
	}

	if _, err := conn.Exec(ctx, "VACUUM ANALYZE"); err != nil {
		return false, err
	}
	_, err = conn.Exec(ctx, "CHECKPOINT")
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" {
		return false, nil
	}
	return err == nil, err
}

// connectTPCC connects to the tpcc database as the DSN's user.
func connectTPCC(ctx context.Context, dsn string) (*pgx.Conn, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	config.Database = "tpcc"
	return pgx.ConnectConfig(ctx, config)
}
