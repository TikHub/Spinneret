package identitysvc

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/identitysvc/identitysvcdb"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/store/postgres/db"
)

// accountCursor is the pagination cursor of ListAccounts.
type accountCursor struct {
	After string `json:"a"`
}

// ListAccounts lists the accounts of the sites the principal may read
// (identity:read).
func (s *Service) ListAccounts(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, q AccountQuery) (AccountPage, error) {
	if s.pool == nil {
		return AccountPage{}, apperr.Internal(errNoPool)
	}
	if !slices.Contains(accountStates, q.State) {
		return AccountPage{}, invalid("state %q must be one of active, banned, disabled", truncateText(q.State, 32))
	}
	if err := validateText("search", q.Search, MaxAccountRefBytes); err != nil {
		return AccountPage{}, err
	}
	visible, err := visibleSites(p, ns, authz.PermIdentityRead)
	if err != nil {
		return AccountPage{}, err
	}
	sites, err := narrowSites(p, ns, visible, q.Site, authz.PermIdentityRead)
	if err != nil {
		return AccountPage{}, err
	}
	var cur accountCursor
	if _, err := decodeCursor(q.PageToken, &cur); err != nil {
		return AccountPage{}, err
	}
	page := AccountPage{Accounts: []Account{}}
	if len(sites) == 0 {
		return page, nil
	}
	limit := pageSize(q.PageSize)
	queries := identitysvcdb.New(s.pool)
	ids := siteIDs(sites)
	rows, err := queries.AccountList(ctx, identitysvcdb.AccountListParams{
		SiteIds: ids, Search: q.Search, State: q.State, AfterID: cur.After, MaxRows: int32(limit + 1),
	})
	if err != nil {
		return AccountPage{}, fmt.Errorf("list accounts: %w", err)
	}
	total, err := queries.AccountCount(ctx, identitysvcdb.AccountCountParams{SiteIds: ids, Search: q.Search, State: q.State})
	if err != nil {
		return AccountPage{}, fmt.Errorf("count accounts: %w", err)
	}
	page.Total = int(total)
	for i, row := range rows {
		if i == limit {
			page.NextPageToken = encodeCursor(accountCursor{After: rows[limit-1].ID})
			break
		}
		page.Accounts = append(page.Accounts, accountView(ns, identitysvcdb.AccountGetRow(row)))
	}
	return page, nil
}

// UpsertAccount creates the account (site, external reference) or updates
// its region, tags and notes (identity:write).
func (s *Service) UpsertAccount(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, in AccountUpsert) (Account, error) {
	if s.pool == nil {
		return Account{}, apperr.Internal(errNoPool)
	}
	site, err := siteByName(ns, in.Site)
	if err != nil {
		return Account{}, err
	}
	if err := requireSite(p, authz.PermIdentityWrite, ns, site); err != nil {
		return Account{}, err
	}
	if in.ExternalRef == "" {
		return Account{}, invalid("external_ref is required")
	}
	if err := validateAccountRef(in.ExternalRef); err != nil {
		return Account{}, err
	}
	if err := validateText("region", in.Region, MaxRegionBytes); err != nil {
		return Account{}, err
	}
	if len(in.Notes) > MaxNotesBytes {
		return Account{}, invalid("notes must be at most %d bytes", MaxNotesBytes)
	}
	if err := validateText("notes", stripNewlines(in.Notes), MaxNotesBytes); err != nil {
		return Account{}, err
	}
	tags, err := validateTags(in.Tags)
	if err != nil {
		return Account{}, err
	}
	if tags == nil {
		tags = []string{}
	}
	var (
		id      string
		created bool
	)
	err = pgstore.InTx(ctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := identitysvcdb.New(tx)
		newID, err := q.AccountInsert(ctx, identitysvcdb.AccountInsertParams{
			ID: idgen.New(idgen.Account), SiteID: site.ID, ExternalRef: in.ExternalRef, Region: in.Region, Tags: tags, Notes: in.Notes,
		})
		switch {
		case err == nil:
			id, created = newID, true
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return pgstore.MapError(err, "account")
		}
		id, err = q.AccountUpdateByRef(ctx, identitysvcdb.AccountUpdateByRefParams{
			SiteID: site.ID, ExternalRef: in.ExternalRef, Region: in.Region, Tags: tags, Notes: in.Notes,
		})
		return pgstore.MapError(err, "account")
	})
	if err != nil {
		return Account{}, err
	}
	details := map[string]any{"site": site.Name, "created": created}
	if !s.syncHot(ctx, site.ID, nil, []string{id}) {
		details["hot_sync_failed"] = true
	}
	s.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, "account.upsert", "account", id, in.ExternalRef,
		audit.ResultOK, details))
	row, err := identitysvcdb.New(s.pool).AccountGet(ctx, id)
	if err != nil {
		return Account{}, pgstore.MapError(err, "account")
	}
	return accountView(ns, row), nil
}

// OperateAccount applies a manual operation (ban, unban, cooldown, disable,
// enable) to an account and its identities (identity:operate) through the
// Operator, and returns the updated account with the effect on identities.
func (s *Service) OperateAccount(ctx context.Context, p *authz.Principal, accountID string, req OperationRequest) (Account, BulkResult, error) {
	if s.pool == nil {
		return Account{}, BulkResult{}, apperr.Internal(errNoPool)
	}
	if s.ops == nil {
		return Account{}, BulkResult{}, apperr.Internal(errNoOperator)
	}
	if err := validateOperation(req, accountOperations, false); err != nil {
		return Account{}, BulkResult{}, err
	}
	q := identitysvcdb.New(s.pool)
	row, err := q.AccountGet(ctx, accountID)
	if err != nil {
		return Account{}, BulkResult{}, pgstore.MapError(err, "account")
	}
	site, ns, err := s.siteByID(row.SiteID, "account")
	if err != nil {
		return Account{}, BulkResult{}, err
	}
	if err := requireByID(p, ns, site, "account", authz.PermIdentityOperate); err != nil {
		return Account{}, BulkResult{}, err
	}
	res, err := s.ops.OperateAccount(ctx, p, ns, accountID, req)
	if err != nil {
		return Account{}, BulkResult{}, err
	}
	row, err = q.AccountGet(ctx, accountID)
	if err != nil {
		return Account{}, BulkResult{}, pgstore.MapError(err, "account")
	}
	return accountView(ns, row), res, nil
}

// accountView converts a stored account.
func accountView(ns *catalog.Namespace, row identitysvcdb.AccountGetRow) Account {
	out := Account{
		ID: row.ID, SiteID: row.SiteID, ExternalRef: row.ExternalRef, Region: row.Region, Tags: row.Tags,
		State: row.State, BanUntil: row.BanUntil, CooldownUntil: row.CooldownUntil, Notes: row.Notes,
		IdentityCount: int(row.IdentityCount), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if site, ok := ns.SitesByID[row.SiteID]; ok {
		out.SiteName = site.Name
	}
	return out
}

// stripNewlines removes line breaks and tabs, which notes may contain, so the
// remaining text can be checked for other control characters.
func stripNewlines(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if c := s[i]; c != '\n' && c != '\r' && c != '\t' {
			out = append(out, c)
		}
	}
	return string(out)
}
