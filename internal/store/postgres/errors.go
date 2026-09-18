package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

// PostgreSQL SQLSTATE codes classified by MapError.
const (
	SQLStateUniqueViolation      = "23505"
	SQLStateForeignKeyViolation  = "23503"
	SQLStateCheckViolation       = "23514"
	SQLStateNotNullViolation     = "23502"
	SQLStateSerializationFailure = "40001"
	SQLStateDeadlockDetected     = "40P01"
)

// MapError converts a database error into an application error. what names
// the affected resource in client-facing messages (for example "site" or
// `site "shop"`):
//
//   - pgx.ErrNoRows → apperr.NotFound("<what> not found")
//   - 23505 unique violation → apperr.AlreadyExists
//   - 23503 foreign key, 23514 check, 23502 not-null violations → apperr.FailedPrecondition
//   - 40001 serialization failure, 40P01 deadlock → apperr.Conflict (retryable)
//   - errors that already are *apperr.Error are returned unchanged
//   - anything else is wrapped with what as context
//
// The original error is kept as the cause for logging. MapError(nil) is nil.
func MapError(err error, what string) error {
	if err == nil {
		return nil
	}
	if what == "" {
		what = "resource"
	}
	if _, ok := apperr.As(err); ok {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound("%s not found", what).WithCause(err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if mapped := mapPgError(pgErr, what); mapped != nil {
			return mapped.WithCause(err)
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// mapPgError classifies a server error, returning nil for codes that are not
// translated into application errors.
func mapPgError(pgErr *pgconn.PgError, what string) *apperr.Error {
	switch pgErr.Code {
	case SQLStateUniqueViolation:
		return apperr.AlreadyExists("%s already exists", what)
	case SQLStateForeignKeyViolation:
		return apperr.FailedPrecondition(apperr.ReasonFailedPrecondition,
			"%s violates reference constraint%s", what, constraintSuffix(pgErr.ConstraintName))
	case SQLStateCheckViolation:
		return apperr.FailedPrecondition(apperr.ReasonFailedPrecondition,
			"%s violates check constraint%s", what, constraintSuffix(pgErr.ConstraintName))
	case SQLStateNotNullViolation:
		if pgErr.ColumnName != "" {
			return apperr.FailedPrecondition(apperr.ReasonFailedPrecondition,
				"%s is missing required value %q", what, pgErr.ColumnName)
		}
		return apperr.FailedPrecondition(apperr.ReasonFailedPrecondition, "%s is missing a required value", what)
	case SQLStateSerializationFailure, SQLStateDeadlockDetected:
		return apperr.Conflict("%s was modified concurrently, retry the operation", what)
	default:
		return nil
	}
}

func constraintSuffix(name string) string {
	if name == "" {
		return ""
	}
	return fmt.Sprintf(" %q", name)
}
