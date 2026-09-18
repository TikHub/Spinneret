package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/auth/authdb"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/pkg/netx"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
)

// siteScopes are the scopes whose argument is a site name.
var siteScopes = map[string]bool{
	authz.ScopeLeaseAcquire:  true,
	authz.ScopeReportWrite:   true,
	authz.ScopeIdentityWrite: true,
}

// NamespaceRef identifies a namespace resolved by the caller.
type NamespaceRef struct {
	ID       string
	TenantID string
	Name     string
}

func (n NamespaceRef) resource() authz.Resource {
	return authz.Resource{TenantID: n.TenantID, NamespaceID: n.ID, NamespaceName: n.Name}
}

// CreateTokenInput describes a new API token.
type CreateTokenInput struct {
	Name         string
	Description  string
	Scopes       []string
	IPAllowlist  []string
	RateLimitRPS int32
	ExpiresAt    *time.Time
}

// Tokens manages API tokens.
type Tokens struct {
	pool   *pgxpool.Pool
	q      *authdb.Queries
	bus    events.Bus
	audit  audit.Recorder
	logger *slog.Logger
	now    func() time.Time
}

// NewTokens creates the token service. bus may be nil (revocations then only
// take effect when caches expire on other instances).
func NewTokens(pool *pgxpool.Pool, bus events.Bus, rec audit.Recorder, logger *slog.Logger) *Tokens {
	if rec == nil {
		rec = audit.Nop{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Tokens{pool: pool, q: authdb.New(pool), bus: bus, audit: rec, logger: logger.With(slog.String("component", "auth")), now: time.Now}
}

// preparedToken is validated token input.
type preparedToken struct {
	// scopes and allow are the canonical stored forms.
	scopes []string
	allow  []string
	// parsed and prefixes are the parsed forms of scopes and allow.
	parsed   []authz.Scope
	prefixes []netip.Prefix
}

// prepareToken validates token input: name, scopes (site-name arguments must
// match the site name pattern), IP allowlist, rate limit and expiry.
func prepareToken(in CreateTokenInput, now time.Time) (preparedToken, error) {
	if err := validateName("name", in.Name, maxTokenNameLength); err != nil {
		return preparedToken{}, err
	}
	if err := validateText("description", in.Description, maxDescriptionLength); err != nil {
		return preparedToken{}, err
	}
	if len(in.Scopes) == 0 {
		return preparedToken{}, apperr.InvalidArgument("", "at least one scope is required")
	}
	if len(in.Scopes) > maxTokenScopes {
		return preparedToken{}, apperr.InvalidArgument("", "at most %d scopes are allowed", maxTokenScopes)
	}
	scopes, err := authz.ParseScopes(in.Scopes)
	if err != nil {
		return preparedToken{}, err
	}
	out := preparedToken{scopes: make([]string, len(scopes)), allow: []string{}, parsed: scopes}
	for i, s := range scopes {
		if siteScopes[s.Name] && s.Arg != "" && !ValidSiteName(s.Arg) {
			return preparedToken{}, apperr.InvalidArgument("", "invalid scope %q: site name argument is not a valid site name", s.Raw)
		}
		out.scopes[i] = s.Raw
	}
	if len(in.IPAllowlist) > maxTokenAllowlist {
		return preparedToken{}, apperr.InvalidArgument("", "at most %d IP allowlist entries are allowed", maxTokenAllowlist)
	}
	prefixes, err := netx.ParsePrefixes(in.IPAllowlist)
	if err != nil {
		return preparedToken{}, apperr.InvalidArgument("", "ip_allowlist: %v", err)
	}
	out.prefixes = prefixes
	for _, p := range prefixes {
		out.allow = append(out.allow, p.String())
	}
	if in.RateLimitRPS < 0 || in.RateLimitRPS > maxTokenRateLimitRPS {
		return preparedToken{}, apperr.InvalidArgument("", "rate_limit_rps must be between 0 and %d", maxTokenRateLimitRPS)
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return preparedToken{}, apperr.InvalidArgument("", "expires_at must be in the future")
	}
	return out, nil
}

// insertToken generates and stores a token.
func insertToken(ctx context.Context, q *authdb.Queries, ns NamespaceRef, in CreateTokenInput, prep preparedToken, createdBy string) (TokenView, string, error) {
	plaintext, err := GenerateToken()
	if err != nil {
		return TokenView{}, "", apperr.Internal(err)
	}
	row, err := q.AuthTokenInsert(ctx, authdb.AuthTokenInsertParams{
		ID: idgen.New(idgen.Token), TenantID: ns.TenantID, NamespaceID: ns.ID, Name: in.Name, Description: in.Description,
		TokenPrefix: plaintext[:TokenPrefixLength], TokenHash: HashToken(plaintext), Scopes: prep.scopes,
		IpAllowlist: prep.allow, RateLimitRps: in.RateLimitRPS, ExpiresAt: in.ExpiresAt, CreatedBy: createdBy,
	})
	if err != nil {
		return TokenView{}, "", internalUnlessApp(pgstore.MapError(err, "token "+strconvQuote(in.Name)))
	}
	return tokenViewFromGetRow(authdb.AuthTokenGetRow{
		ID: row.ID, TenantID: row.TenantID, NamespaceID: row.NamespaceID, NamespaceName: ns.Name, Name: row.Name,
		Description: row.Description, TokenPrefix: row.TokenPrefix, Scopes: row.Scopes, IpAllowlist: row.IpAllowlist,
		RateLimitRps: row.RateLimitRps, ExpiresAt: row.ExpiresAt, RevokedAt: row.RevokedAt, LastUsedAt: row.LastUsedAt,
		LastUsedIp: row.LastUsedIp, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
	}), plaintext, nil
}

// Create creates an API token bound to ns (token:write) and returns it with
// the plaintext, which is never stored or shown again. When p is an API token
// the new token cannot be broader than p (see checkTokenCreatedByToken).
func (t *Tokens) Create(ctx context.Context, p *authz.Principal, ns NamespaceRef, in CreateTokenInput) (*TokenView, string, error) {
	if err := p.Require(authz.PermTokenWrite, ns.resource()); err != nil {
		return nil, "", err
	}
	prep, err := prepareToken(in, t.now())
	if err != nil {
		return nil, "", err
	}
	if err := t.checkTokenCreatedByToken(ctx, p, in, prep); err != nil {
		return nil, "", err
	}
	view, plaintext, err := insertToken(ctx, t.q, ns, in, prep, p.Actor())
	if err != nil {
		return nil, "", err
	}
	t.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, ActionTokenCreate, ResourceToken, view.ID, view.Name,
		audit.ResultOK, map[string]any{
			"scopes": view.Scopes, "ip_allowlist": view.IPAllowlist, "rate_limit_rps": view.RateLimitRPS,
			"token_prefix": view.TokenPrefix, "expires_at": view.ExpiresAt,
		}))
	return &view, plaintext, nil
}

// Revoke revokes a token of the principal's tenant (token:write on its
// namespace) and notifies every instance to drop it from caches. Revoking an
// already revoked token is a no-op.
func (t *Tokens) Revoke(ctx context.Context, p *authz.Principal, id string) (*TokenView, error) {
	row, err := t.visibleToken(ctx, p, id)
	if err != nil {
		return nil, err
	}
	ns := NamespaceRef{ID: row.NamespaceID, TenantID: row.TenantID, Name: row.NamespaceName}
	if err := p.Require(authz.PermTokenWrite, ns.resource()); err != nil {
		return nil, err
	}
	already := row.RevokedAt != nil
	revokedAt, err := t.q.AuthTokenRevoke(ctx, row.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.NotFound("token not found")
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	row.RevokedAt = revokedAt
	t.publishRevoked(ctx, row.TenantID, row.NamespaceID, row.ID)
	if !already {
		t.audit.Record(ctx, audit.FromPrincipal(p, row.TenantID, row.NamespaceID, ActionTokenRevoke, ResourceToken,
			row.ID, row.Name, audit.ResultOK, map[string]any{"token_prefix": row.TokenPrefix}))
	}
	view := tokenViewFromGetRow(row)
	return &view, nil
}

// visibleToken loads a token that belongs to the principal's tenant.
func (t *Tokens) visibleToken(ctx context.Context, p *authz.Principal, id string) (authdb.AuthTokenGetRow, error) {
	if p == nil {
		return authdb.AuthTokenGetRow{}, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	row, err := t.q.AuthTokenGet(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return authdb.AuthTokenGetRow{}, apperr.NotFound("token not found")
	}
	if err != nil {
		return authdb.AuthTokenGetRow{}, apperr.Internal(err)
	}
	if !tenantVisible(p, row.TenantID) {
		return authdb.AuthTokenGetRow{}, apperr.NotFound("token not found")
	}
	return row, nil
}

func (t *Tokens) publishRevoked(ctx context.Context, tenantID, namespaceID, tokenID string) {
	if t.bus == nil {
		return
	}
	raw, err := json.Marshal(tokensEventData{TokenID: tokenID})
	if err != nil {
		t.logger.Error("encode token revocation", slog.Any("error", err))
		return
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), redisTimeout)
	defer cancel()
	ev := events.Event{Type: EventTokenRevoked, TenantID: tenantID, NamespaceID: namespaceID, Data: raw}
	if err := t.bus.Publish(pctx, events.ChannelTokens, ev); err != nil {
		t.logger.Warn("publish token revocation failed; peers drop the token when their cache expires",
			slog.String("token_id", tokenID), slog.Any("error", err))
	}
}

// ListTokensInput filters and pages tokens.
type ListTokensInput struct {
	// Namespace restricts the listing; nil lists every namespace of the
	// active tenant in which the principal holds token:read.
	Namespace      *NamespaceRef
	IncludeRevoked bool
	PageSize       int32
	PageToken      string
}

type tokenCursor struct {
	CreatedAt int64  `json:"t"`
	ID        string `json:"i"`
}

// List lists tokens, newest first.
func (t *Tokens) List(ctx context.Context, p *authz.Principal, in ListTokensInput) ([]TokenView, string, int32, error) {
	nsIDs, err := t.readableNamespaces(ctx, p, in.Namespace)
	if err != nil {
		return nil, "", 0, err
	}
	var cur tokenCursor
	hasCursor, err := apiutil.DecodeCursor(in.PageToken, &cur)
	if err != nil {
		return nil, "", 0, err
	}
	size := apiutil.PageSize(in.PageSize)
	rows, err := t.q.AuthTokenList(ctx, authdb.AuthTokenListParams{
		NamespaceIds: nsIDs, IncludeRevoked: in.IncludeRevoked, HasCursor: hasCursor,
		CursorCreatedAt: time.UnixMicro(cur.CreatedAt).UTC(), CursorID: cur.ID, PageLimit: int32(size + 1),
	})
	if err != nil {
		return nil, "", 0, apperr.Internal(err)
	}
	total, err := t.q.AuthTokenCount(ctx, authdb.AuthTokenCountParams{NamespaceIds: nsIDs, IncludeRevoked: in.IncludeRevoked})
	if err != nil {
		return nil, "", 0, apperr.Internal(err)
	}
	next := ""
	if len(rows) > size {
		rows = rows[:size]
		last := rows[len(rows)-1]
		if next, err = apiutil.EncodeCursor(tokenCursor{CreatedAt: last.CreatedAt.UnixMicro(), ID: last.ID}); err != nil {
			return nil, "", 0, apperr.Internal(err)
		}
	}
	out := make([]TokenView, len(rows))
	for i, r := range rows {
		out[i] = tokenViewFromGetRow(authdb.AuthTokenGetRow(r))
	}
	return out, next, total, nil
}

// readableNamespaces returns the namespace IDs whose tokens p may read.
func (t *Tokens) readableNamespaces(ctx context.Context, p *authz.Principal, ns *NamespaceRef) ([]string, error) {
	if p == nil {
		return nil, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	if ns != nil {
		if err := p.Require(authz.PermTokenRead, ns.resource()); err != nil {
			return nil, err
		}
		return []string{ns.ID}, nil
	}
	tenantID, err := activeTenant(p)
	if err != nil {
		return nil, err
	}
	rows, err := t.q.AuthNamespacesOfTenant(ctx, tenantID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	var ids []string
	for _, r := range rows {
		if p.Can(authz.PermTokenRead, NamespaceRef{ID: r.ID, TenantID: r.TenantID, Name: r.Name}.resource()) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		// Nothing readable: tenant-wide token readers of a tenant without
		// namespaces get an empty listing, everyone else a denial.
		if err := p.Require(authz.PermTokenRead, authz.Resource{TenantID: tenantID}); err != nil {
			return nil, err
		}
		return []string{}, nil
	}
	return ids, nil
}

// CreateTokenDirect creates a token without a principal (used by the CLI to
// bootstrap nodes). The namespace must belong to the tenant.
func CreateTokenDirect(ctx context.Context, pool *pgxpool.Pool, tenantID, namespaceID, name string, scopes []string, expiresAt *time.Time) (string, string, error) {
	q := authdb.New(pool)
	ns, err := q.AuthNamespaceGet(ctx, namespaceID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && ns.TenantID != tenantID) {
		return "", "", apperr.NotFound("namespace not found in tenant")
	}
	if err != nil {
		return "", "", apperr.Internal(err)
	}
	in := CreateTokenInput{Name: strings.TrimSpace(name), Scopes: scopes, ExpiresAt: expiresAt}
	prep, err := prepareToken(in, time.Now())
	if err != nil {
		return "", "", err
	}
	view, plaintext, err := insertToken(ctx, q, NamespaceRef{ID: ns.ID, TenantID: ns.TenantID, Name: ns.Name}, in, prep, "system:cli")
	if err != nil {
		return "", "", err
	}
	return plaintext, view.ID, nil
}
