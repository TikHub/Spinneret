package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/store/postgres"
)

func TestMapError(t *testing.T) {
	plain := errors.New("connection reset")
	existing := apperr.InvalidArgument(apperr.ReasonURIInvalid, "bad uri")

	tests := []struct {
		name        string
		err         error
		what        string
		wantNil     bool
		wantSame    bool
		wantCode    connect.Code
		wantReason  apperr.Reason
		wantMessage string
		wantPlain   string
	}{
		{name: "nil", err: nil, what: "site", wantNil: true},
		{
			name: "no rows", err: pgx.ErrNoRows, what: `site "shop"`,
			wantCode: connect.CodeNotFound, wantReason: apperr.ReasonNotFound, wantMessage: `site "shop" not found`,
		},
		{
			name: "wrapped no rows", err: fmt.Errorf("load: %w", pgx.ErrNoRows), what: "identity",
			wantCode: connect.CodeNotFound, wantReason: apperr.ReasonNotFound, wantMessage: "identity not found",
		},
		{
			name: "empty what", err: pgx.ErrNoRows, what: "",
			wantCode: connect.CodeNotFound, wantReason: apperr.ReasonNotFound, wantMessage: "resource not found",
		},
		{
			name: "unique violation", err: &pgconn.PgError{Code: "23505", ConstraintName: "sites_namespace_id_name_key"}, what: "site",
			wantCode: connect.CodeAlreadyExists, wantReason: apperr.ReasonAlreadyExists, wantMessage: "site already exists",
		},
		{
			name: "foreign key violation", err: &pgconn.PgError{Code: "23503", ConstraintName: "identities_type_id_fkey"}, what: "identity type",
			wantCode: connect.CodeFailedPrecondition, wantReason: apperr.ReasonFailedPrecondition,
			wantMessage: `identity type violates reference constraint "identities_type_id_fkey"`,
		},
		{
			name: "foreign key violation without constraint", err: &pgconn.PgError{Code: "23503"}, what: "proxy",
			wantCode: connect.CodeFailedPrecondition, wantReason: apperr.ReasonFailedPrecondition,
			wantMessage: "proxy violates reference constraint",
		},
		{
			name: "check violation", err: &pgconn.PgError{Code: "23514", ConstraintName: "proxies_port_check"}, what: "proxy",
			wantCode: connect.CodeFailedPrecondition, wantReason: apperr.ReasonFailedPrecondition,
			wantMessage: `proxy violates check constraint "proxies_port_check"`,
		},
		{
			name: "not null violation with column", err: &pgconn.PgError{Code: "23502", ColumnName: "host"}, what: "proxy",
			wantCode: connect.CodeFailedPrecondition, wantReason: apperr.ReasonFailedPrecondition,
			wantMessage: `proxy is missing required value "host"`,
		},
		{
			name: "not null violation without column", err: &pgconn.PgError{Code: "23502"}, what: "proxy",
			wantCode: connect.CodeFailedPrecondition, wantReason: apperr.ReasonFailedPrecondition,
			wantMessage: "proxy is missing a required value",
		},
		{
			name: "serialization failure", err: fmt.Errorf("commit: %w", &pgconn.PgError{Code: "40001"}), what: "policy",
			wantCode: connect.CodeAborted, wantReason: apperr.ReasonConflict,
			wantMessage: "policy was modified concurrently, retry the operation",
		},
		{
			name: "deadlock", err: &pgconn.PgError{Code: "40P01"}, what: "identity",
			wantCode: connect.CodeAborted, wantReason: apperr.ReasonConflict,
			wantMessage: "identity was modified concurrently, retry the operation",
		},
		{name: "unclassified pg error", err: &pgconn.PgError{Code: "42P01", Message: "relation missing"}, what: "site", wantPlain: "site: "},
		{name: "plain error", err: plain, what: "load site", wantPlain: "load site: connection reset"},
		{name: "context canceled", err: context.Canceled, what: "list", wantPlain: "list: context canceled"},
		{name: "application error passthrough", err: existing, what: "site", wantSame: true},
		{name: "wrapped application error passthrough", err: fmt.Errorf("ctx: %w", existing), what: "site", wantSame: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := postgres.MapError(tc.err, tc.what)
			switch {
			case tc.wantNil:
				require.NoError(t, got)
			case tc.wantSame:
				require.Same(t, tc.err, got)
			case tc.wantPlain != "":
				require.Error(t, got)
				_, isApp := apperr.As(got)
				require.False(t, isApp, "must not become an application error")
				require.ErrorIs(t, got, tc.err)
				require.Contains(t, got.Error(), tc.wantPlain)
			default:
				appErr, ok := apperr.As(got)
				require.True(t, ok, "want *apperr.Error, got %T", got)
				require.Equal(t, tc.wantCode, appErr.Code)
				require.Equal(t, tc.wantReason, appErr.Reason)
				require.Equal(t, tc.wantMessage, appErr.Message)
				require.ErrorIs(t, got, tc.err, "cause must be preserved")
			}
		})
	}
}
