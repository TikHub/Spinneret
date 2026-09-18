package vault

import (
	"context"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/vault/vaultdb"
)

// maxListFilterTags bounds the tags of a list filter.
const maxListFilterTags = 32

// ListSecretsOptions filters and pages ListSecrets. Secrets are ordered by
// path; AfterPath is the keyset cursor (the path of the last secret of the
// previous page).
type ListSecretsOptions struct {
	// Prefix keeps secrets whose path starts with it.
	Prefix string
	// Search keeps secrets whose path or description contains it (case-insensitive).
	Search string
	// Tags keeps secrets carrying every listed tag.
	Tags      []string
	Limit     int
	AfterPath string
}

// SecretPage is one page of secrets.
type SecretPage struct {
	Secrets []Secret
	// Total counts every secret matching the filter (ignoring the cursor).
	Total   int64
	HasMore bool
}

// SecretVersionInfo is the metadata of one secret version.
type SecretVersionInfo struct {
	Version   int
	KEKID     string
	CreatedBy string
	CreatedAt time.Time
}

// ListVersionsOptions pages ListVersions (newest first). BeforeVersion is the
// keyset cursor (the last version of the previous page, 0 for the first page).
type ListVersionsOptions struct {
	Limit         int
	BeforeVersion int
}

// SecretVersionPage is one page of secret versions.
type SecretVersionPage struct {
	Versions []SecretVersionInfo
	Total    int64
	HasMore  bool
}

// SecretAccessLog is one audited operation on a secret.
type SecretAccessLog struct {
	ID        string
	CreatedAt time.Time
	ActorKind string
	ActorID   string
	ActorName string
	Action    string
	Result    string
	IP        string
	// Version accessed; 0 when not applicable.
	Version int
}

// AccessLogCursor is the keyset cursor of ListAccessLogs: the creation time
// and ID of the last entry of the previous page.
type AccessLogCursor struct {
	CreatedAt time.Time
	ID        string
}

// ListAccessLogsOptions pages ListAccessLogs (newest first).
type ListAccessLogsOptions struct {
	Limit int
	After *AccessLogCursor
}

// SecretAccessLogPage is one page of access logs.
type SecretAccessLogPage struct {
	Logs    []SecretAccessLog
	HasMore bool
}

// List returns a page of secret metadata with masked values (secret:list).
func (s *SecretStore) List(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, opts ListSecretsOptions) (SecretPage, error) {
	if ns == nil {
		return SecretPage{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "namespace is required")
	}
	if p == nil {
		return SecretPage{}, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	if err := validateListOptions(&opts); err != nil {
		return SecretPage{}, err
	}
	if err := p.Require(authz.PermSecretList, secretResource(ns.TenantID, ns.ID, ns.Name, "")); err != nil {
		return SecretPage{}, err
	}
	ctx, cancel := secretOpContext(ctx)
	defer cancel()

	limit := pageLimit(opts.Limit)
	rows, err := s.q.VaultSecretList(ctx, vaultdb.VaultSecretListParams{
		NamespaceID: ns.ID, Prefix: opts.Prefix, Search: opts.Search, Tags: opts.Tags,
		AfterPath: opts.AfterPath, LimitRows: int32(limit + 1),
	})
	if err != nil {
		return SecretPage{}, pgstore.MapError(err, "secrets")
	}
	total, err := s.q.VaultSecretCount(ctx, vaultdb.VaultSecretCountParams{
		NamespaceID: ns.ID, Prefix: opts.Prefix, Search: opts.Search, Tags: opts.Tags,
	})
	if err != nil {
		return SecretPage{}, pgstore.MapError(err, "secrets")
	}
	page := SecretPage{Total: total, Secrets: make([]Secret, 0, min(len(rows), limit))}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	failures := maskFailures{namespace: ns.ID}
	for _, row := range rows {
		masked, err := s.maskEnvelope(row.ID, row.CurrentVersion, row.Ciphertext, row.WrappedDek, row.KekID)
		failures.add(row.ID, err)
		page.Secrets = append(page.Secrets, Secret{
			ID: row.ID, TenantID: ns.TenantID, NamespaceID: row.NamespaceID, NamespaceName: ns.Name,
			Path: row.Path, Description: row.Description, Tags: nonNilTags(row.Tags),
			CurrentVersion: int(row.CurrentVersion),
			MaskedValue:    masked,
			ExpiresAt:      row.ExpiresAt, LastAccessedAt: row.LastAccessedAt, CreatedBy: row.CreatedBy,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	failures.log(s.logger)
	return page, nil
}

func validateListOptions(opts *ListSecretsOptions) error {
	switch {
	case len(opts.Prefix) > MaxSecretPathLength || !utf8.ValidString(opts.Prefix):
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "prefix must be valid UTF-8 of at most %d bytes", MaxSecretPathLength)
	case len(opts.AfterPath) > MaxSecretPathLength || !utf8.ValidString(opts.AfterPath):
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "invalid page cursor")
	case !utf8.ValidString(opts.Search) || utf8.RuneCountInString(opts.Search) > MaxSecretSearchLength:
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "search must be valid UTF-8 of at most %d characters", MaxSecretSearchLength)
	}
	tags, err := normalizeTags(opts.Tags, maxListFilterTags)
	if err != nil {
		return err
	}
	opts.Tags = tags
	return nil
}

// ListVersions returns a page of version metadata, newest first (secret:list).
func (s *SecretStore) ListVersions(ctx context.Context, p *authz.Principal, id string, opts ListVersionsOptions) (SecretVersionPage, error) {
	if opts.BeforeVersion < 0 {
		return SecretVersionPage{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "invalid page cursor")
	}
	ctx, cancel := secretOpContext(ctx)
	defer cancel()
	if _, err := s.loadAuthorized(ctx, p, id, authz.PermSecretList); err != nil {
		return SecretVersionPage{}, err
	}
	limit := pageLimit(opts.Limit)
	rows, err := s.q.VaultSecretVersionList(ctx, vaultdb.VaultSecretVersionListParams{
		SecretID: id, BeforeVersion: int32(min(opts.BeforeVersion, math.MaxInt32)), LimitRows: int32(limit + 1),
	})
	if err != nil {
		return SecretVersionPage{}, pgstore.MapError(err, "secret versions")
	}
	total, err := s.q.VaultSecretVersionCount(ctx, id)
	if err != nil {
		return SecretVersionPage{}, pgstore.MapError(err, "secret versions")
	}
	page := SecretVersionPage{Total: total, Versions: make([]SecretVersionInfo, 0, min(len(rows), limit))}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	for _, row := range rows {
		page.Versions = append(page.Versions, SecretVersionInfo{
			Version: int(row.Version), KEKID: row.KekID, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
		})
	}
	return page, nil
}

// ListAccessLogs returns a page of the audit entries of a secret (reads,
// reveals and changes), newest first. It requires audit:read or secret:list.
func (s *SecretStore) ListAccessLogs(ctx context.Context, p *authz.Principal, id string, opts ListAccessLogsOptions) (SecretAccessLogPage, error) {
	ctx, cancel := secretOpContext(ctx)
	defer cancel()
	row, err := s.loadAuthorized(ctx, p, id, authz.PermSecretList, authz.PermAuditRead)
	if err != nil {
		return SecretAccessLogPage{}, err
	}
	limit := pageLimit(opts.Limit)
	params := vaultdb.VaultSecretAccessLogsParams{
		TenantID: row.TenantID, SecretID: id, Since: row.CreatedAt.Add(-accessLogLookback),
		LimitRows: int32(limit + 1), CursorCreatedAt: time.Unix(0, 0).UTC(),
	}
	if opts.After != nil {
		params.HasCursor, params.CursorCreatedAt, params.CursorID = true, opts.After.CreatedAt, opts.After.ID
	}
	rows, err := s.q.VaultSecretAccessLogs(ctx, params)
	if err != nil {
		return SecretAccessLogPage{}, pgstore.MapError(err, "secret access logs")
	}
	page := SecretAccessLogPage{Logs: make([]SecretAccessLog, 0, min(len(rows), limit))}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	for _, r := range rows {
		version, _ := strconv.Atoi(r.Version)
		page.Logs = append(page.Logs, SecretAccessLog{
			ID: r.ID, CreatedAt: r.CreatedAt, ActorKind: r.ActorKind, ActorID: r.ActorID, ActorName: r.ActorName,
			Action: r.Action, Result: r.Result, IP: r.Ip, Version: max(version, 0),
		})
	}
	return page, nil
}
