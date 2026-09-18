package auth

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/auth/authdb"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres/db"
)

// maxUserQueryLength bounds the ListUsers substring filter.
const maxUserQueryLength = 256

// CreateUserInput describes a new user and its initial role binding in the
// principal's active tenant.
type CreateUserInput struct {
	Username    string
	DisplayName string
	Email       string
	Password    string
	Binding     BindingSpec
}

// CreateUserResult is the created (or, for platform admins, existing) user and
// the new binding.
type CreateUserResult struct {
	User    UserView
	Binding BindingView
	// Existing is true when the username already existed and only a binding
	// was added (platform administrators only).
	Existing bool
}

// CreateUser creates a user with an initial role binding in the active tenant
// (user:write). When the username already exists, platform administrators get
// the binding added to the existing account; everyone else gets
// already_exists.
func (u *Users) CreateUser(ctx context.Context, p *authz.Principal, in CreateUserInput) (*CreateUserResult, error) {
	tenantID, err := activeTenant(p)
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermUserWrite, authz.Resource{TenantID: tenantID}); err != nil {
		return nil, err
	}
	spec, err := normalizeBindingSpec(in.Binding)
	if err != nil {
		return nil, err
	}
	if spec.Role == string(authz.RoleOwner) && !tenantOwner(p, tenantID) {
		return nil, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "only tenant owners can grant the owner role")
	}
	username := NormalizeUsername(in.Username)
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if err := validateText("display_name", in.DisplayName, maxDisplayNameLength); err != nil {
		return nil, err
	}
	if err := validateEmail(in.Email); err != nil {
		return nil, err
	}
	existing, err := u.q.AuthUserByUsername(ctx, username)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Internal(err)
	}
	if exists && !superuser(p) {
		return nil, apperr.AlreadyExists("user %q already exists", username)
	}
	var hashed string
	if !exists {
		if hashed, err = u.hash(ctx, in.Password); err != nil {
			return nil, err
		}
	}
	var result CreateUserResult
	err = pgstore.InTx(ctx, u.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := authdb.New(tx)
		if err := validateBindingTargets(ctx, q, tenantID, spec); err != nil {
			return err
		}
		user := existing
		if !exists {
			created, err := q.AuthUserInsert(ctx, authdb.AuthUserInsertParams{
				ID: newUserID(), Username: username, DisplayName: in.DisplayName, Email: in.Email, PasswordHash: hashed,
			})
			if err != nil {
				return pgstore.MapError(err, "user "+strconvQuote(username))
			}
			user = created
		} else if err := rejectDuplicateBinding(ctx, q, user.ID, tenantID, spec); err != nil {
			return err
		}
		binding, err := insertBinding(ctx, q, p, user.ID, tenantID, spec)
		if err != nil {
			return err
		}
		result = CreateUserResult{User: userViewFromModel(user), Binding: binding, Existing: exists}
		return nil
	})
	if err != nil {
		return nil, internalUnlessApp(err)
	}
	if err := fillSiteNames(ctx, u.q, []*BindingView{&result.Binding}); err != nil {
		return nil, apperr.Internal(err)
	}
	result.Binding.Username = result.User.Username
	u.publishUserChanged(ctx, result.User.ID)
	action := ActionUserCreate
	if exists {
		action = ActionBindingCreate
	}
	u.audit.Record(ctx, audit.FromPrincipal(p, tenantID, spec.NamespaceID, action, ResourceUser, result.User.ID,
		result.User.Username, audit.ResultOK, bindingDetails(result.Binding)))
	return &result, nil
}

// UpdateUserInput holds optional profile changes; nil fields are kept.
type UpdateUserInput struct {
	DisplayName *string
	Email       *string
	Locale      *string
	Disabled    *bool
}

// UpdateUser changes a user's profile or disabled flag (user:write in the
// active tenant; the user must be a member of it and must not hold bindings in
// tenants the caller cannot manage). Disabling ends every session.
func (u *Users) UpdateUser(ctx context.Context, p *authz.Principal, userID string, in UpdateUserInput) (*UserView, error) {
	target, err := u.manageableUser(ctx, p, userID)
	if err != nil {
		return nil, err
	}
	details := map[string]any{}
	if in.DisplayName != nil {
		if err := validateText("display_name", *in.DisplayName, maxDisplayNameLength); err != nil {
			return nil, err
		}
		details["display_name"] = true
	}
	if in.Email != nil {
		if err := validateEmail(*in.Email); err != nil {
			return nil, err
		}
		details["email"] = true
	}
	if in.Locale != nil {
		if err := validateLocale(*in.Locale); err != nil {
			return nil, err
		}
		details["locale"] = *in.Locale
	}
	if in.Disabled != nil {
		if *in.Disabled && p.Kind == authz.KindUser && p.ID == target.ID {
			return nil, apperr.FailedPrecondition("", "you cannot disable your own account")
		}
		details["disabled"] = *in.Disabled
	}
	updated, err := u.q.AuthUserUpdate(ctx, authdb.AuthUserUpdateParams{
		ID: target.ID, DisplayName: in.DisplayName, Email: in.Email, Locale: in.Locale, Disabled: in.Disabled,
	})
	if err != nil {
		return nil, internalUnlessApp(pgstore.MapError(err, "user"))
	}
	if updated.Disabled && !target.Disabled {
		if _, err := u.sessions.revokeUser(ctx, updated.ID, ""); err != nil {
			return nil, apperr.Internal(err)
		}
	}
	u.publishUserChanged(ctx, updated.ID)
	u.audit.Record(ctx, audit.FromPrincipal(p, p.TenantID, "", ActionUserUpdate, ResourceUser, updated.ID,
		updated.Username, audit.ResultOK, details))
	view := userViewFromModel(updated)
	return &view, nil
}

// ResetPassword sets a new password for a manageable user and ends all of its
// sessions.
func (u *Users) ResetPassword(ctx context.Context, p *authz.Principal, userID, password string) error {
	target, err := u.manageableUser(ctx, p, userID)
	if err != nil {
		return err
	}
	hashed, err := u.hash(ctx, password)
	if err != nil {
		return err
	}
	_, err = u.q.AuthUserSetPassword(ctx, authdb.AuthUserSetPasswordParams{ID: target.ID, PasswordHash: hashed})
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound("user not found")
	}
	if err != nil {
		return apperr.Internal(err)
	}
	// Sessions created with the previous password are rejected by their
	// password generation; revoking them also frees their storage.
	if _, err := u.sessions.revokeUser(ctx, target.ID, ""); err != nil {
		return apperr.Internal(err)
	}
	u.publishUserChanged(ctx, target.ID)
	if err := u.throttle.reset(ctx, target.Username); err != nil {
		u.logger.Warn("reset login throttle failed", slog.String("user_id", target.ID), slog.Any("error", err))
	}
	u.audit.Record(ctx, audit.FromPrincipal(p, p.TenantID, "", ActionUserResetPassword, ResourceUser, target.ID,
		target.Username, audit.ResultOK, nil))
	return nil
}

// manageableUser loads a user the principal may administer.
func (u *Users) manageableUser(ctx context.Context, p *authz.Principal, userID string) (authdb.User, error) {
	if p == nil {
		return authdb.User{}, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	var tenantID string
	if !superuser(p) {
		var err error
		if tenantID, err = activeTenant(p); err != nil {
			return authdb.User{}, err
		}
		if err := p.Require(authz.PermUserWrite, authz.Resource{TenantID: tenantID}); err != nil {
			return authdb.User{}, err
		}
	}
	target, err := u.q.AuthUserByID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return authdb.User{}, apperr.NotFound("user not found")
	}
	if err != nil {
		return authdb.User{}, apperr.Internal(err)
	}
	if superuser(p) {
		return target, nil
	}
	bindings, err := u.q.AuthBindingsOfUser(ctx, target.ID)
	if err != nil {
		return authdb.User{}, apperr.Internal(err)
	}
	member := slices.ContainsFunc(bindings, func(b authdb.RoleBinding) bool { return b.TenantID == tenantID })
	if !member {
		return authdb.User{}, apperr.NotFound("user not found")
	}
	if target.IsPlatformAdmin {
		return authdb.User{}, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "platform administrators can only be managed by platform administrators")
	}
	for _, b := range bindings {
		if !p.Can(authz.PermUserWrite, authz.Resource{TenantID: b.TenantID}) {
			return authdb.User{}, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "the user belongs to tenants you cannot manage")
		}
	}
	return target, nil
}

// ListUsersInput filters and pages users.
type ListUsersInput struct {
	Query     string
	AllUsers  bool
	PageSize  int32
	PageToken string
}

type userCursor struct {
	Username string `json:"u"`
}

// ListUsers lists the members of the active tenant with their bindings in it
// (user:read). Platform administrators may set AllUsers to list every user
// with bindings in every tenant.
func (u *Users) ListUsers(ctx context.Context, p *authz.Principal, in ListUsersInput) ([]TenantUser, string, int32, error) {
	var tenantID string
	if in.AllUsers {
		if !superuser(p) {
			return nil, "", 0, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "listing all users requires a platform administrator")
		}
	} else {
		var err error
		if tenantID, err = activeTenant(p); err != nil {
			return nil, "", 0, err
		}
		if err := p.Require(authz.PermUserRead, authz.Resource{TenantID: tenantID}); err != nil {
			return nil, "", 0, err
		}
	}
	query := strings.TrimSpace(in.Query)
	if len(query) > maxUserQueryLength {
		return nil, "", 0, apperr.InvalidArgument("", "query must be at most %d bytes", maxUserQueryLength)
	}
	var cur userCursor
	if _, err := apiutil.DecodeCursor(in.PageToken, &cur); err != nil {
		return nil, "", 0, err
	}
	size := apiutil.PageSize(in.PageSize)
	rows, err := u.q.AuthUserListMembers(ctx, authdb.AuthUserListMembersParams{
		AllUsers: in.AllUsers, TenantID: tenantID, Query: query, AfterUsername: cur.Username, PageLimit: int32(size + 1),
	})
	if err != nil {
		return nil, "", 0, apperr.Internal(err)
	}
	total, err := u.q.AuthUserCountMembers(ctx, authdb.AuthUserCountMembersParams{AllUsers: in.AllUsers, TenantID: tenantID, Query: query})
	if err != nil {
		return nil, "", 0, apperr.Internal(err)
	}
	next := ""
	if len(rows) > size {
		rows = rows[:size]
		if next, err = apiutil.EncodeCursor(userCursor{Username: rows[len(rows)-1].Username}); err != nil {
			return nil, "", 0, apperr.Internal(err)
		}
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	bindingRows, err := u.q.AuthBindingsOfUsers(ctx, authdb.AuthBindingsOfUsersParams{UserIds: ids, TenantID: tenantID})
	if err != nil {
		return nil, "", 0, apperr.Internal(err)
	}
	views := make([]BindingView, len(bindingRows))
	ptrs := make([]*BindingView, len(bindingRows))
	byUser := make(map[string][]int, len(rows))
	for i, r := range bindingRows {
		views[i] = bindingRow(r).view()
		ptrs[i] = &views[i]
		byUser[r.UserID] = append(byUser[r.UserID], i)
	}
	if err := fillSiteNames(ctx, u.q, ptrs); err != nil {
		return nil, "", 0, apperr.Internal(err)
	}
	out := make([]TenantUser, len(rows))
	for i, r := range rows {
		tu := TenantUser{User: userViewFromListRow(r), Bindings: make([]BindingView, 0, len(byUser[r.ID]))}
		for _, idx := range byUser[r.ID] {
			tu.Bindings = append(tu.Bindings, views[idx])
		}
		out[i] = tu
	}
	return out, next, total, nil
}

// internalUnlessApp keeps application errors and wraps everything else as internal.
func internalUnlessApp(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := apperr.As(err); ok {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return apperr.Internal(err)
}
