package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CopyFromer is the COPY FROM capability shared by *pgxpool.Pool, *pgx.Conn
// and pgx.Tx.
type CopyFromer interface {
	CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error)
}

// CopyRows bulk-inserts rows into table with the COPY protocol and returns the
// number of rows copied. table may be schema-qualified ("schema.table");
// values maps one row to its column values in the order of columns. An empty
// rows slice is a no-op. COPY cannot resolve conflicts, so use it for
// append-only tables (events, logs) and upserts for aggregates.
func CopyRows[T any](ctx context.Context, c CopyFromer, table string, columns []string, rows []T, values func(T) []any) (int64, error) {
	if c == nil {
		return 0, errors.New("copy rows: destination is nil")
	}
	if table == "" || len(columns) == 0 {
		return 0, fmt.Errorf("copy rows into %q: table and columns are required", table)
	}
	if values == nil {
		return 0, fmt.Errorf("copy rows into %s: values function is nil", table)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	src := pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
		v := values(rows[i])
		if len(v) != len(columns) {
			return nil, fmt.Errorf("row %d has %d values, want %d", i, len(v), len(columns))
		}
		return v, nil
	})
	n, err := c.CopyFrom(ctx, pgx.Identifier(strings.Split(table, ".")), columns, src)
	if err != nil {
		return n, fmt.Errorf("copy rows into %s: %w", table, err)
	}
	return n, nil
}
