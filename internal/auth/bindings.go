package auth

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/auth/authdb"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres/db"
)

// BindingSpec describes a role binding inside the active tenant. Names are
// resolved to IDs by the caller (the API layer resolves them in the catalog).
type BindingSpec struct {
	Role string
	// NamespaceID narrows the binding to one namespace; "" = all namespaces.
	NamespaceID string
	// SiteIDs narrows the binding to sites of NamespaceID; empty = all sites.
	SiteIDs []string
	// ExtraPermissions are added to the role (config:publish, secret:reveal,
	// identity:reveal, policy:publish).
	ExtraPermissions []string
}

// normalizeBindingSpec validates a spec and returns a copy with sorted,
// de-duplicated site IDs and extra permissions.
func normalizeBindingSpec(s BindingSpec) (BindingSpec, error) {
	if !authz.ValidRole(authz.Role(s.Role)) {
		return BindingSpec{}, apperr.InvalidArgument("", "role must be one of owner, admin, operator, viewer")
	}
	if len(s.SiteIDs) > 0 && s.NamespaceID == "" {
		return BindingSpec{}, apperr.InvalidArgument("", "sites require a namespace")
	}
	if len(s.SiteIDs) > maxBindingSites {
		return BindingSpec{}, apperr.InvalidArgument("", "at most %d sites can be bound", maxBindingSites)
	}
	out := BindingSpec{Role: s.Role, NamespaceID: s.NamespaceID}
	out.SiteIDs = sortedUnique(s.SiteIDs)
	out.ExtraPermissions = sortedUnique(s.ExtraPermissions)
	for _, id := range out.SiteIDs {
		if id == "" {
			return BindingSpec{}, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site IDs must not be empty")
		}
	}
	for _, perm := range out.ExtraPermissions {
		if !authz.ValidExtraPermission(authz.Permission(perm)) {
			return BindingSpec{}, apperr.InvalidArgument("", "extra permission %q is not allowed", perm)
		}
	}
	return out, nil
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// validateBindingTargets checks that the namespace belongs to the tenant and
// every site belongs to the namespace.
func validateBindingTargets(ctx context.Context, q *authdb.Queries, tenantID string, s BindingSpec) error {
	if s.NamespaceID == "" {
		return nil
	}
	ns, err := q.AuthNamespaceGet(ctx, s.NamespaceID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && ns.TenantID != tenantID) {
		return apperr.NotFound("namespace not found")
	}
	if err != nil {
		return err
	}
	if len(s.SiteIDs) == 0 {
		return nil
	}
	rows, err := q.AuthSitesByIDs(ctx, s.SiteIDs)
	if err != nil {
		return err
	}
	found := make(map[string]bool, len(rows))
	for _, r := range rows {
		if r.NamespaceID == s.NamespaceID {
			found[r.ID] = true
		}
	}
	for _, id := range s.SiteIDs {
		if !found[id] {
			return apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site %q not found in namespace %q", id, ns.Name)
		}
	}
	return nil
}

// rejectDuplicateBinding fails when the user already holds an identical binding.
func rejectDuplicateBinding(ctx context.Context, q *authdb.Queries, userID, tenantID string, s BindingSpec) error {
	rows, err := q.AuthBindingsOfUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		ns := ""
		if r.NamespaceID != nil {
			ns = *r.NamespaceID
		}
		if r.TenantID == tenantID && r.Role == s.Role && ns == s.NamespaceID &&
			slices.Equal(sortedUnique(r.SiteIds), s.SiteIDs) && slices.Equal(sortedUnique(r.ExtraPermissions), s.ExtraPermissions) {
			return apperr.AlreadyExists("an identical role binding already exists")
		}
	}
	return nil
}

// insertBinding stores a validated binding.
func insertBinding(ctx context.Context, q *authdb.Queries, p *authz.Principal, userID, tenantID string, s BindingSpec) (BindingView, error) {
	var nsID *string
	if s.NamespaceID != "" {
		nsID = &s.NamespaceID
	}
	row, err := q.AuthBindingInsert(ctx, authdb.AuthBindingInsertParams{
		ID: idgen.New(idgen.RoleBinding), UserID: userID, TenantID: tenantID, Role: s.Role, NamespaceID: nsID,
		SiteIds: s.SiteIDs, ExtraPermissions: s.ExtraPermissions, CreatedBy: p.Actor(),
	})
	if err != nil {
		return BindingView{}, pgstore.MapError(err, "role binding")
	}
	view := bindingRow{
		ID: row.ID, UserID: row.UserID, TenantID: row.TenantID, Role: row.Role, NamespaceID: row.NamespaceID,
		SiteIds: row.SiteIds, ExtraPermissions: row.ExtraPermissions, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
	}.view()
	if nsID != nil {
		ns, err := q.AuthNamespaceGet(ctx, *nsID)
		if err != nil {
			return BindingView{}, pgstore.MapError(err, "namespace")
		}
		view.Namespace = ns.Name
	}
	return view, nil
}

// fillSiteNames resolves the site names of binding views.
func fillSiteNames(ctx context.Context, q *authdb.Queries, views []*BindingView) error {
	var ids []string
	for _, v := range views {
		ids = append(ids, v.SiteIDs...)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := q.AuthSitesByIDs(ctx, sortedUnique(ids))
	if err != nil {
		return err
	}
	names := make(map[string]string, len(rows))
	for _, r := range rows {
		names[r.ID] = r.Name
	}
	for _, v := range views {
		v.Sites = make([]string, len(v.SiteIDs))
		for i, id := range v.SiteIDs {
			v.Sites[i] = names[id]
		}
	}
	return nil
}

func bindingDetails(b BindingView) map[string]any {
	return map[string]any{
		"binding_id":        b.ID,
		"role":              b.Role,
		"namespace":         b.Namespace,
		"sites":             b.Sites,
		"extra_permissions": b.ExtraPermissions,
	}
}

func newUserID() string { return idgen.New(idgen.User) }

func strconvQuote(s string) string { return strconv.Quote(s) }

type bindingCursor struct {
	Username  string `json:"u"`
	CreatedAt int64  `json:"t"`
	ID        string `json:"i"`
}

// ListRoleBindings lists the role bindings of the active tenant (user:read),
// optionally of one user, ordered by username and creation time.
func (u *Users) ListRoleBindings(ctx context.Context, p *authz.Principal, userID string, pageSize int32, pageToken string) ([]BindingView, string, int32, error) {
	tenantID, err := activeTenant(p)
	if err != nil {
		return nil, "", 0, err
	}
	if err := p.Require(authz.PermUserRead, authz.Resource{TenantID: tenantID}); err != nil {
		return nil, "", 0, err
	}
	var cur bindingCursor
	hasCursor, err := apiutil.DecodeCursor(pageToken, &cur)
	if err != nil {
		return nil, "", 0, err
	}
	size := apiutil.PageSize(pageSize)
	rows, err := u.q.AuthBindingList(ctx, authdb.AuthBindingListParams{
		TenantID: tenantID, UserID: userID, HasCursor: hasCursor, CursorUsername: cur.Username,
		CursorCreatedAt: time.UnixMicro(cur.CreatedAt).UTC(), CursorID: cur.ID, PageLimit: int32(size + 1),
	})
	if err != nil {
		return nil, "", 0, apperr.Internal(err)
	}
	total, err := u.q.AuthBindingCount(ctx, authdb.AuthBindingCountParams{TenantID: tenantID, UserID: userID})
	if err != nil {
		return nil, "", 0, apperr.Internal(err)
	}
	next := ""
	if len(rows) > size {
		rows = rows[:size]
		last := rows[len(rows)-1]
		next, err = apiutil.EncodeCursor(bindingCursor{Username: last.Username, CreatedAt: last.CreatedAt.UnixMicro(), ID: last.ID})
		if err != nil {
			return nil, "", 0, apperr.Internal(err)
		}
	}
	views := make([]BindingView, len(rows))
	ptrs := make([]*BindingView, len(rows))
	for i, r := range rows {
		views[i] = bindingRow(r).view()
		ptrs[i] = &views[i]
	}
	if err := fillSiteNames(ctx, u.q, ptrs); err != nil {
		return nil, "", 0, apperr.Internal(err)
	}
	return views, next, total, nil
}

// CreateRoleBinding grants a role in the active tenant to an existing user
// (user:write; granting owner requires a tenant owner).
func (u *Users) CreateRoleBinding(ctx context.Context, p *authz.Principal, userID string, spec BindingSpec) (*BindingView, error) {
	tenantID, err := activeTenant(p)
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermUserWrite, authz.Resource{TenantID: tenantID}); err != nil {
		return nil, err
	}
	spec, err = normalizeBindingSpec(spec)
	if err != nil {
		return nil, err
	}
	if spec.Role == string(authz.RoleOwner) && !tenantOwner(p, tenantID) {
		return nil, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "only tenant owners can grant the owner role")
	}
	var view BindingView
	err = pgstore.InTx(ctx, u.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := authdb.New(tx)
		user, err := q.AuthUserByID(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.NotFound("user not found")
		}
		if err != nil {
			return err
		}
		if err := validateBindingTargets(ctx, q, tenantID, spec); err != nil {
			return err
		}
		if err := rejectDuplicateBinding(ctx, q, user.ID, tenantID, spec); err != nil {
			return err
		}
		if view, err = insertBinding(ctx, q, p, user.ID, tenantID, spec); err != nil {
			return err
		}
		view.Username = user.Username
		return nil
	})
	if err != nil {
		return nil, internalUnlessApp(err)
	}
	if err := fillSiteNames(ctx, u.q, []*BindingView{&view}); err != nil {
		return nil, apperr.Internal(err)
	}
	u.publishUserChanged(ctx, view.UserID)
	u.audit.Record(ctx, audit.FromPrincipal(p, tenantID, spec.NamespaceID, ActionBindingCreate, ResourceRoleBinding,
		view.ID, view.Username, audit.ResultOK, bindingDetails(view)))
	return &view, nil
}

// DeleteRoleBinding removes a role binding of the active tenant (user:write).
// Only tenant owners may remove owner bindings, and the last tenant-wide owner
// binding of a tenant cannot be removed.
func (u *Users) DeleteRoleBinding(ctx context.Context, p *authz.Principal, bindingID string) error {
	tenantID, err := activeTenant(p)
	if err != nil {
		return err
	}
	if err := p.Require(authz.PermUserWrite, authz.Resource{TenantID: tenantID}); err != nil {
		return err
	}
	got, err := u.q.AuthBindingGet(ctx, bindingID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && got.TenantID != tenantID) {
		return apperr.NotFound("role binding not found")
	}
	if err != nil {
		return apperr.Internal(err)
	}
	row := bindingRow(got)
	if row.Role == string(authz.RoleOwner) && !tenantOwner(p, tenantID) {
		return apperr.PermissionDenied(apperr.ReasonPermissionDenied, "only tenant owners can remove owner bindings")
	}
	tenantWideOwner := row.Role == string(authz.RoleOwner) && row.NamespaceID == nil && len(row.SiteIds) == 0
	err = pgstore.InTx(ctx, u.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := authdb.New(tx)
		if tenantWideOwner {
			owners, err := q.AuthTenantOwnerBindingsForUpdate(ctx, tenantID)
			if err != nil {
				return err
			}
			if slices.Contains(owners, row.ID) && len(owners) <= 1 {
				return apperr.FailedPrecondition("", "cannot delete the last owner binding of the tenant")
			}
		}
		n, err := q.AuthBindingDelete(ctx, row.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.NotFound("role binding not found")
		}
		return nil
	})
	if err != nil {
		return internalUnlessApp(err)
	}
	view := row.view()
	if err := fillSiteNames(ctx, u.q, []*BindingView{&view}); err != nil {
		u.logger.Warn("resolve site names for audit", slog.String("binding_id", row.ID), slog.Any("error", err))
	}
	u.publishUserChanged(ctx, row.UserID)
	nsID := ""
	if row.NamespaceID != nil {
		nsID = *row.NamespaceID
	}
	u.audit.Record(ctx, audit.FromPrincipal(p, tenantID, nsID, ActionBindingDelete, ResourceRoleBinding, row.ID,
		row.Username, audit.ResultOK, bindingDetails(view)))
	return nil
}
