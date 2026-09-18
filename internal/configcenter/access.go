package configcenter

import (
	"context"
	"slices"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/configcenter/configdb"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

// Audit actions and resource kind written by the config center.
const (
	ActionCreate       = "config.create"
	ActionSaveDraft    = "config.draft.save"
	ActionPublish      = "config.publish"
	ActionRollback     = "config.rollback"
	ActionDelete       = "config.delete"
	ActionRead         = "config.read"
	AuditResourceKind  = "config_item"
	itemNotFoundFormat = "config item not found"
)

func isAppErr(err error) bool {
	_, ok := apperr.As(err)
	return ok
}

// configResource builds the authorization resource of a config group.
func configResource(ns *catalog.Namespace, group string) authz.Resource {
	return authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name, ConfigGroup: group}
}

func itemName(group, key string) string {
	return group + "/" + key
}

// requirePrincipal rejects a nil principal.
func requirePrincipal(p *authz.Principal) error {
	if p == nil {
		return apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	return nil
}

// requireCaller rejects a nil principal or namespace.
func requireCaller(p *authz.Principal, ns *catalog.Namespace) error {
	if err := requirePrincipal(p); err != nil {
		return err
	}
	if ns == nil {
		return apperr.InvalidArgument("", "namespace is required")
	}
	return nil
}

// requireMutation checks perm on the group and records a denied audit entry
// when the check fails.
func (s *Service) requireMutation(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, perm authz.Permission,
	action, itemID, group, key string,
) error {
	err := p.Require(perm, configResource(ns, group))
	if err != nil {
		s.record(ctx, p, ns, action, itemID, group, key, audit.ResultDenied, map[string]any{"permission": string(perm)})
	}
	return err
}

// record writes an audit entry for an item operation.
func (s *Service) record(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, action, itemID, group, key, result string,
	details map[string]any,
) {
	s.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, action, AuditResourceKind, itemID, itemName(group, key), result, details))
}

// holdsAnyConfigRead reports whether the principal holds config:read for at
// least some group of the namespace (tokens may be restricted to group globs).
func holdsAnyConfigRead(p *authz.Principal, ns *catalog.Namespace) bool {
	if p.Can(authz.PermConfigRead, configResource(ns, "")) {
		return true
	}
	if p.Kind != authz.KindToken || p.TenantID != ns.TenantID || p.NamespaceID != ns.ID {
		return false
	}
	for _, sc := range p.Scopes {
		if slices.Contains(sc.Permissions(), authz.PermConfigRead) {
			return true
		}
	}
	return false
}

// visibleNamespace reports whether objects of ns may be addressed by ID by
// the principal: tokens only see their own namespace and users their active
// tenant (platform admins and system principals see everything). Invisible
// objects are reported as not found.
func visibleNamespace(p *authz.Principal, ns *catalog.Namespace) bool {
	switch p.Kind {
	case authz.KindSystem:
		return true
	case authz.KindToken:
		return p.TenantID == ns.TenantID && p.NamespaceID == ns.ID
	case authz.KindUser:
		return p.IsPlatformAdmin || p.TenantID == ns.TenantID
	default:
		return false
	}
}

// itemHeader is the identity of a stored item used for authorization.
type itemHeader struct {
	ID     string
	Group  string
	Key    string
	Format string
	NS     *catalog.Namespace
}

// loadHeader loads an item by ID and resolves its namespace. Items that do
// not exist or are not visible to the principal are not_found.
func (s *Service) loadHeader(ctx context.Context, p *authz.Principal, id string) (itemHeader, error) {
	if err := requirePrincipal(p); err != nil {
		return itemHeader{}, err
	}
	if !idgen.Valid(id, idgen.ConfigItem) {
		return itemHeader{}, apperr.NotFound(itemNotFoundFormat)
	}
	ctx, cancel := s.opContext(ctx)
	defer cancel()
	row, err := s.queries().ConfigGetItemHeader(ctx, id)
	if err != nil {
		return itemHeader{}, pgstore.MapError(err, "config item")
	}
	ns, ok := s.cat.Namespace(row.NamespaceID)
	if !ok || !visibleNamespace(p, ns) {
		return itemHeader{}, apperr.NotFound(itemNotFoundFormat)
	}
	return headerFromRow(row, ns), nil
}

func headerFromRow(row configdb.ConfigGetItemHeaderRow, ns *catalog.Namespace) itemHeader {
	return itemHeader{ID: row.ID, Group: row.GroupName, Key: row.Key, Format: row.Format, NS: ns}
}
