package auth

import (
	"time"

	"github.com/Evil0ctal/Spinneret/internal/auth/authdb"
)

// UserView is a console user without credentials.
type UserView struct {
	ID              string
	Username        string
	DisplayName     string
	Email           string
	Locale          string
	IsPlatformAdmin bool
	Disabled        bool
	LastLoginAt     *time.Time
	CreatedAt       time.Time
}

// BindingView is a role binding with resolved user, namespace and site names.
type BindingView struct {
	ID               string
	UserID           string
	Username         string
	TenantID         string
	Role             string
	NamespaceID      string // "" = every namespace
	Namespace        string
	SiteIDs          []string // empty = every site
	Sites            []string // names matching SiteIDs ("" for deleted sites)
	ExtraPermissions []string
	CreatedBy        string
	CreatedAt        time.Time
}

// TenantView describes a tenant.
type TenantView struct {
	ID          string
	Name        string
	DisplayName string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NamespaceView describes a namespace.
type NamespaceView struct {
	ID          string
	TenantID    string
	Name        string
	DisplayName string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SiteAccess lists the effective permissions on one site granted beyond the
// namespace-wide set.
type SiteAccess struct {
	SiteID      string
	SiteName    string
	Permissions []string
}

// NamespaceAccess lists the effective permissions inside one namespace.
type NamespaceAccess struct {
	Namespace   NamespaceView
	Permissions []string
	Sites       []SiteAccess
}

// TenantAccess summarizes what a user can do inside one tenant.
type TenantAccess struct {
	Tenant     TenantView
	Bindings   []BindingView
	Namespaces []NamespaceAccess
}

// Me is the current user with its accessible tenants.
type Me struct {
	User    UserView
	Tenants []TenantAccess
}

// TenantUser is a user with its role bindings.
type TenantUser struct {
	User     UserView
	Bindings []BindingView
}

// TokenView describes an API token without its secret.
type TokenView struct {
	ID           string
	TenantID     string
	NamespaceID  string
	Namespace    string
	Name         string
	Description  string
	TokenPrefix  string
	Scopes       []string
	IPAllowlist  []string
	RateLimitRPS int32
	ExpiresAt    *time.Time
	RevokedAt    *time.Time
	LastUsedAt   *time.Time
	LastUsedIP   string
	CreatedBy    string
	CreatedAt    time.Time
}

func userViewFromModel(u authdb.User) UserView {
	return UserView{
		ID:              u.ID,
		Username:        u.Username,
		DisplayName:     u.DisplayName,
		Email:           u.Email,
		Locale:          u.Locale,
		IsPlatformAdmin: u.IsPlatformAdmin,
		Disabled:        u.Disabled,
		LastLoginAt:     u.LastLoginAt,
		CreatedAt:       u.CreatedAt,
	}
}

func userViewFromListRow(u authdb.AuthUserListMembersRow) UserView {
	return UserView{
		ID:              u.ID,
		Username:        u.Username,
		DisplayName:     u.DisplayName,
		Email:           u.Email,
		Locale:          u.Locale,
		IsPlatformAdmin: u.IsPlatformAdmin,
		Disabled:        u.Disabled,
		LastLoginAt:     u.LastLoginAt,
		CreatedAt:       u.CreatedAt,
	}
}

func tenantViewFromModel(t authdb.Tenant) TenantView {
	return TenantView{
		ID:          t.ID,
		Name:        t.Name,
		DisplayName: t.DisplayName,
		Description: t.Description,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
}

func namespaceViewFromModel(n authdb.Namespace) NamespaceView {
	return NamespaceView{
		ID:          n.ID,
		TenantID:    n.TenantID,
		Name:        n.Name,
		DisplayName: n.DisplayName,
		Description: n.Description,
		CreatedAt:   n.CreatedAt,
		UpdatedAt:   n.UpdatedAt,
	}
}

// bindingRow is the common shape of the binding queries joined with users and
// namespaces (AuthBindingGetRow, AuthBindingListRow, AuthBindingsOfUsersRow).
type bindingRow struct {
	ID               string
	UserID           string
	Username         string
	TenantID         string
	Role             string
	NamespaceID      *string
	NamespaceName    string
	SiteIds          []string
	ExtraPermissions []string
	CreatedBy        string
	CreatedAt        time.Time
}

// bindingView converts a row; site names are filled in by the caller.
func (r bindingRow) view() BindingView {
	v := BindingView{
		ID:               r.ID,
		UserID:           r.UserID,
		Username:         r.Username,
		TenantID:         r.TenantID,
		Role:             r.Role,
		Namespace:        r.NamespaceName,
		SiteIDs:          nonNil(r.SiteIds),
		Sites:            make([]string, len(r.SiteIds)),
		ExtraPermissions: nonNil(r.ExtraPermissions),
		CreatedBy:        r.CreatedBy,
		CreatedAt:        r.CreatedAt,
	}
	if r.NamespaceID != nil {
		v.NamespaceID = *r.NamespaceID
	}
	return v
}

func tokenViewFromGetRow(r authdb.AuthTokenGetRow) TokenView {
	return TokenView{
		ID:           r.ID,
		TenantID:     r.TenantID,
		NamespaceID:  r.NamespaceID,
		Namespace:    r.NamespaceName,
		Name:         r.Name,
		Description:  r.Description,
		TokenPrefix:  r.TokenPrefix,
		Scopes:       nonNil(r.Scopes),
		IPAllowlist:  nonNil(r.IpAllowlist),
		RateLimitRPS: r.RateLimitRps,
		ExpiresAt:    r.ExpiresAt,
		RevokedAt:    r.RevokedAt,
		LastUsedAt:   r.LastUsedAt,
		LastUsedIP:   r.LastUsedIp,
		CreatedBy:    r.CreatedBy,
		CreatedAt:    r.CreatedAt,
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
