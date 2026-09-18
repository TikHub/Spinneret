package auth

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth/authdb"
	"github.com/TikHub/Spinneret/internal/authz"
)

const (
	// defaultAuditRange is the time range queried when none is given.
	defaultAuditRange = 24 * time.Hour
	// auditClockSkew extends the default end bound past "now" to include
	// entries timestamped by instances with slightly fast clocks.
	auditClockSkew = time.Minute
	// maxAuditFilterLength bounds string filters.
	maxAuditFilterLength = 256
)

// AuditQuery filters audit log entries. Empty strings match everything.
type AuditQuery struct {
	// Namespace restricts entries to one namespace; nil queries the active
	// tenant: every entry with a tenant-wide audit:read, otherwise the entries
	// of the namespaces in which the principal holds audit:read.
	Namespace    *NamespaceRef
	Actor        string // actor ID or actor name
	Action       string
	ResourceKind string
	ResourceID   string
	Result       string
	// Start and End bound [Start, End); nil defaults to the last 24 hours.
	Start     *time.Time
	End       *time.Time
	PageSize  int32
	PageToken string
}

// AuditRecord is one audit log entry.
type AuditRecord struct {
	ID           string
	CreatedAt    time.Time
	TenantID     string
	NamespaceID  string
	Namespace    string
	ActorKind    string
	ActorID      string
	ActorName    string
	Action       string
	ResourceKind string
	ResourceID   string
	ResourceName string
	Result       string
	IP           string
	UserAgent    string
	Details      map[string]any
}

// AuditLogs queries the audit log.
type AuditLogs struct {
	q   *authdb.Queries
	now func() time.Time
}

// NewAuditLogs creates the audit log reader.
func NewAuditLogs(pool *pgxpool.Pool) *AuditLogs {
	return &AuditLogs{q: authdb.New(pool), now: time.Now}
}

type auditCursor struct {
	CreatedAt int64  `json:"t"`
	ID        string `json:"i"`
}

// auditScope is the part of the audit log a principal may read.
type auditScope struct {
	tenantID string
	// namespaceID is the explicit namespace filter ("" = none).
	namespaceID string
	// all disables the namespaceIDs restriction.
	all bool
	// namespaceIDs lists the readable namespaces when all is false.
	namespaceIDs []string
}

// List returns audit entries of the active tenant, newest first, using keyset
// pagination over (created_at, id). It requires audit:read on the namespace
// filter; without one, principals with a tenant-wide audit:read see every
// entry of the tenant and others the entries of the namespaces they can read
// the audit log of (permission denied when there are none). Platform
// administrators without an active tenant see platform-level entries with an
// empty tenant ID (such as sign-ins).
func (l *AuditLogs) List(ctx context.Context, p *authz.Principal, in AuditQuery) ([]AuditRecord, string, error) {
	scope, err := l.scope(ctx, p, in.Namespace)
	if err != nil {
		return nil, "", err
	}
	for _, f := range []string{in.Actor, in.Action, in.ResourceKind, in.ResourceID, in.Result} {
		if len(f) > maxAuditFilterLength {
			return nil, "", apperr.InvalidArgument("", "filters must be at most %d bytes", maxAuditFilterLength)
		}
	}
	now := l.now()
	start, end := now.Add(-defaultAuditRange), now.Add(auditClockSkew)
	if in.Start != nil {
		start = *in.Start
	}
	if in.End != nil {
		end = *in.End
	}
	if !end.After(start) {
		return nil, "", apperr.InvalidArgument("", "time_range.end must be after time_range.start")
	}
	var cur auditCursor
	hasCursor, err := apiutil.DecodeCursor(in.PageToken, &cur)
	if err != nil {
		return nil, "", err
	}
	size := apiutil.PageSize(in.PageSize)
	rows, err := l.q.AuthAuditList(ctx, authdb.AuthAuditListParams{
		TenantID: scope.tenantID, StartAt: start, EndAt: end, NamespaceID: scope.namespaceID,
		AllNamespaces: scope.all, NamespaceIds: scope.namespaceIDs, Actor: in.Actor, Action: in.Action,
		ResourceKind: in.ResourceKind, ResourceID: in.ResourceID, Result: in.Result, HasCursor: hasCursor,
		CursorCreatedAt: time.UnixMicro(cur.CreatedAt).UTC(), CursorID: cur.ID, PageLimit: int32(size + 1),
	})
	if err != nil {
		return nil, "", apperr.Internal(err)
	}
	next := ""
	if len(rows) > size {
		rows = rows[:size]
		last := rows[len(rows)-1]
		if next, err = apiutil.EncodeCursor(auditCursor{CreatedAt: last.CreatedAt.UnixMicro(), ID: last.ID}); err != nil {
			return nil, "", apperr.Internal(err)
		}
	}
	names, err := l.namespaceNames(ctx, rows)
	if err != nil {
		return nil, "", apperr.Internal(err)
	}
	out := make([]AuditRecord, len(rows))
	for i, r := range rows {
		rec := AuditRecord{
			ID: r.ID, CreatedAt: r.CreatedAt, TenantID: r.TenantID, NamespaceID: r.NamespaceID, Namespace: names[r.NamespaceID],
			ActorKind: r.ActorKind, ActorID: r.ActorID, ActorName: r.ActorName, Action: r.Action, ResourceKind: r.ResourceKind,
			ResourceID: r.ResourceID, ResourceName: r.ResourceName, Result: r.Result, IP: r.Ip, UserAgent: r.UserAgent,
			Details: map[string]any{},
		}
		if len(r.Details) > 0 {
			// Details are written by the server; an unparseable value is dropped
			// rather than failing the whole page.
			_ = json.Unmarshal(r.Details, &rec.Details)
		}
		out[i] = rec
	}
	return out, next, nil
}

// scope resolves which entries p may list.
func (l *AuditLogs) scope(ctx context.Context, p *authz.Principal, ns *NamespaceRef) (auditScope, error) {
	if p == nil {
		return auditScope{}, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	if ns != nil {
		if err := p.Require(authz.PermAuditRead, ns.resource()); err != nil {
			return auditScope{}, err
		}
		return auditScope{tenantID: ns.TenantID, namespaceID: ns.ID, all: true}, nil
	}
	if p.TenantID == "" && superuser(p) {
		return auditScope{all: true}, nil
	}
	tenantID, err := activeTenant(p)
	if err != nil {
		return auditScope{}, err
	}
	tenantRes := authz.Resource{TenantID: tenantID}
	if p.Can(authz.PermAuditRead, tenantRes) {
		return auditScope{tenantID: tenantID, all: true}, nil
	}
	rows, err := l.q.AuthNamespacesOfTenant(ctx, tenantID)
	if err != nil {
		return auditScope{}, apperr.Internal(err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		if p.Can(authz.PermAuditRead, NamespaceRef{ID: r.ID, TenantID: r.TenantID, Name: r.Name}.resource()) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return auditScope{}, p.Require(authz.PermAuditRead, tenantRes)
	}
	return auditScope{tenantID: tenantID, namespaceIDs: ids}, nil
}

func (l *AuditLogs) namespaceNames(ctx context.Context, rows []authdb.AuditLog) (map[string]string, error) {
	var ids []string
	for _, r := range rows {
		if r.NamespaceID != "" {
			ids = append(ids, r.NamespaceID)
		}
	}
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	found, err := l.q.AuthNamespaceNames(ctx, sortedUnique(ids))
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(found))
	for _, n := range found {
		names[n.ID] = n.Name
	}
	return names, nil
}
