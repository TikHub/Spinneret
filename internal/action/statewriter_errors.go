package action

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

// SQLSTATE codes and classes used to classify state write failures.
const (
	sqlStateQueryCanceled        = "57014"
	sqlStateProgramLimitExceeded = "54000"
)

// isStateDBUnavailable reports whether err means that PostgreSQL cannot
// accept writes right now, so the batch must be kept and retried later:
// connection failures, network errors and timeouts on the client side, and
// server errors of the SQLSTATE classes 08 (connection exception), 53
// (insufficient resources), 57 (operator intervention, except a canceled
// statement) and 58 (system error). Client-side errors that are not
// connection related (for example encoding failures) are not included, so a
// malformed batch can never block the writer forever.
func isStateDBUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if len(pgErr.Code) < 2 || pgErr.Code == sqlStateQueryCanceled {
			return false
		}
		switch pgErr.Code[:2] {
		case "08", "53", "57", "58":
			return true
		default:
			return false
		}
	}
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &connectErr),
		errors.As(err, &netErr),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed),
		pgconn.Timeout(err), pgconn.SafeToRetry(err):
		return true
	}
	// pgx reports a connection that broke under a query as an unexported
	// "conn closed" error.
	return strings.Contains(err.Error(), "conn closed")
}

// isStateDataError reports whether PostgreSQL rejected the written values
// themselves (SQLSTATE classes 22 and 23, program limit exceeded), so the
// same rows cannot succeed when retried unchanged.
func isStateDataError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23") ||
		pgErr.Code == sqlStateProgramLimitExceeded
}

// isMissingPartition reports whether err is PostgreSQL's "no partition of
// relation … found for row" (a check violation without a constraint name),
// which the partition manager may still resolve, so it is retried before the
// batch is salvaged.
func isMissingPartition(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != postgres.SQLStateCheckViolation {
		return false
	}
	return pgErr.ConstraintName == "" || strings.Contains(pgErr.Message, "no partition of relation")
}
