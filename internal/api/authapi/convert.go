package authapi

import (
	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/auth"
)

// UserProto converts a user view into its API message.
func UserProto(u auth.UserView) *spinneretv1.User {
	return &spinneretv1.User{
		Id:              u.ID,
		Username:        u.Username,
		DisplayName:     u.DisplayName,
		Email:           u.Email,
		Locale:          u.Locale,
		IsPlatformAdmin: u.IsPlatformAdmin,
		Disabled:        u.Disabled,
		LastLoginAt:     apiutil.TimestampPtr(u.LastLoginAt),
		CreatedAt:       apiutil.Timestamp(u.CreatedAt),
	}
}

// RoleBindingProto converts a binding view into its API message.
func RoleBindingProto(b auth.BindingView) *spinneretv1.RoleBinding {
	return &spinneretv1.RoleBinding{
		Id:               b.ID,
		UserId:           b.UserID,
		Username:         b.Username,
		TenantId:         b.TenantID,
		Role:             b.Role,
		NamespaceId:      b.NamespaceID,
		Namespace:        b.Namespace,
		SiteIds:          b.SiteIDs,
		Sites:            b.Sites,
		ExtraPermissions: b.ExtraPermissions,
		CreatedAt:        apiutil.Timestamp(b.CreatedAt),
	}
}

// RoleBindingProtos converts binding views.
func RoleBindingProtos(bs []auth.BindingView) []*spinneretv1.RoleBinding {
	out := make([]*spinneretv1.RoleBinding, len(bs))
	for i, b := range bs {
		out[i] = RoleBindingProto(b)
	}
	return out
}

// TenantAccessProtos converts the tenant access summaries of Me.
func TenantAccessProtos(ts []auth.TenantAccess) []*spinneretv1.TenantAccess {
	out := make([]*spinneretv1.TenantAccess, len(ts))
	for i, t := range ts {
		ta := &spinneretv1.TenantAccess{
			Tenant: &spinneretv1.Tenant{
				Id: t.Tenant.ID, Name: t.Tenant.Name, DisplayName: t.Tenant.DisplayName, Description: t.Tenant.Description,
				CreatedAt: apiutil.Timestamp(t.Tenant.CreatedAt), UpdatedAt: apiutil.Timestamp(t.Tenant.UpdatedAt),
			},
			Bindings:   RoleBindingProtos(t.Bindings),
			Namespaces: make([]*spinneretv1.NamespaceAccess, len(t.Namespaces)),
		}
		for j, n := range t.Namespaces {
			na := &spinneretv1.NamespaceAccess{
				Namespace: &spinneretv1.Namespace{
					Id: n.Namespace.ID, TenantId: n.Namespace.TenantID, Name: n.Namespace.Name,
					DisplayName: n.Namespace.DisplayName, Description: n.Namespace.Description,
					CreatedAt: apiutil.Timestamp(n.Namespace.CreatedAt), UpdatedAt: apiutil.Timestamp(n.Namespace.UpdatedAt),
				},
				Permissions: n.Permissions,
				Sites:       make([]*spinneretv1.SiteAccess, len(n.Sites)),
			}
			for k, s := range n.Sites {
				na.Sites[k] = &spinneretv1.SiteAccess{SiteId: s.SiteID, SiteName: s.SiteName, Permissions: s.Permissions}
			}
			ta.Namespaces[j] = na
		}
		out[i] = ta
	}
	return out
}
