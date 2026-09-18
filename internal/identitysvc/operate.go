package identitysvc

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/identitysvc/identitysvcdb"
)

// resolveBatchSize is the page size used to resolve filter matches to IDs.
const resolveBatchSize = 5000

// Failure reasons reported by identitysvc before operations reach the Operator.
const (
	FailureNotFound             = "not_found"
	FailureEndpointGroupUnknown = "endpoint_group_unknown"
)

// OperateIdentities applies a manual operation to explicit identities
// (identity:operate on the site of every identity). Unknown IDs, and IDs of
// namespaces the principal has no relation to, are reported as not_found
// failures; an identity on a site the principal may not operate fails the
// whole request with permission_denied. Identities are passed to the Operator
// grouped by namespace.
func (s *Service) OperateIdentities(ctx context.Context, p *authz.Principal, ids []string, req OperationRequest) (BulkResult, error) {
	if s.pool == nil {
		return BulkResult{}, apperr.Internal(errNoPool)
	}
	if s.ops == nil {
		return BulkResult{}, apperr.Internal(errNoOperator)
	}
	if err := validateIDs(ids); err != nil {
		return BulkResult{}, err
	}
	if err := validateOperation(req, identityOperations, true); err != nil {
		return BulkResult{}, err
	}
	located, err := identitysvcdb.New(s.pool).IdentityLocateMany(ctx, ids)
	if err != nil {
		return BulkResult{}, fmt.Errorf("locate identities: %w", err)
	}
	siteOfID := make(map[string]string, len(located))
	for _, l := range located {
		siteOfID[l.ID] = l.SiteID
	}
	var (
		result     = BulkResult{Failed: []BulkFailure{}}
		groups     = map[string][]string{}
		namespaces = map[string]*catalog.Namespace{}
		order      []string
		// groupKnown tracks whether the endpoint group of an identity_endpoint
		// operation exists in the namespace of any found identity.
		groupKnown, anyFound bool
	)
	for _, id := range ids {
		site, ns, err := s.operableSite(p, siteOfID[id])
		if err != nil {
			if apperr.IsNotFound(err) {
				result.Failed = append(result.Failed, BulkFailure{ID: id, Reason: FailureNotFound, Message: "identity not found"})
				continue
			}
			return BulkResult{}, err
		}
		anyFound = true
		if req.Scope == "identity_endpoint" {
			if _, ok := site.GroupsByID[req.EndpointGroupID]; !ok {
				groupKnown = groupKnown || namespaceHasGroup(ns, req.EndpointGroupID)
				result.Failed = append(result.Failed, BulkFailure{ID: id, Reason: FailureEndpointGroupUnknown,
					Message: "the endpoint group does not belong to the site of the identity"})
				continue
			}
			groupKnown = true
		}
		if _, ok := groups[ns.ID]; !ok {
			order = append(order, ns.ID)
			namespaces[ns.ID] = ns
		}
		groups[ns.ID] = append(groups[ns.ID], id)
	}
	if req.Scope == "identity_endpoint" && anyFound && !groupKnown {
		return BulkResult{}, apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown, "endpoint group %q not found", truncateText(req.EndpointGroupID, 64))
	}
	for _, nsID := range order {
		res, err := s.ops.OperateIdentities(ctx, p, namespaces[nsID], groups[nsID], req)
		if err != nil {
			return BulkResult{}, err
		}
		result.merge(res)
	}
	return result, nil
}

// operableSite resolves the site of an identity and checks identity:operate.
// Missing identities (siteID "") and unrelated namespaces yield not_found.
func (s *Service) operableSite(p *authz.Principal, siteID string) (*catalog.Site, *catalog.Namespace, error) {
	if siteID == "" {
		return nil, nil, apperr.NotFound("identity not found")
	}
	site, ns, err := s.siteByID(siteID, "identity")
	if err != nil {
		return nil, nil, err
	}
	if err := requireByID(p, ns, site, "identity", authz.PermIdentityOperate); err != nil {
		return nil, nil, err
	}
	return site, ns, nil
}

// namespaceHasGroup reports whether an endpoint group belongs to a site of ns.
func namespaceHasGroup(ns *catalog.Namespace, groupID string) bool {
	return groupSite(ns, groupID) != nil
}

// groupSite returns the site of ns owning an endpoint group, or nil.
func groupSite(ns *catalog.Namespace, groupID string) *catalog.Site {
	for _, site := range ns.SitesByID {
		if _, ok := site.GroupsByID[groupID]; ok {
			return site
		}
	}
	return nil
}

// validateIDs checks explicit identity IDs.
func validateIDs(ids []string) error {
	if len(ids) == 0 || len(ids) > MaxOperateIDs {
		return invalid("between 1 and %d ids are required", MaxOperateIDs)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > 64 {
			return invalid("ids must be non-empty and at most 64 bytes")
		}
		if _, dup := seen[id]; dup {
			return invalid("ids must be unique (%q is repeated)", truncateText(id, 64))
		}
		seen[id] = struct{}{}
	}
	return nil
}

// BulkOperateIdentities applies a manual operation to every identity matching
// filter on the sites the principal may operate (identity:operate), at most
// limit identities (0 = MaxBulkIdentities). IDs are resolved in keyset
// batches and passed to the Operator in chunks of OperateChunkSize. With
// dryRun only the number of matching identities is returned.
func (s *Service) BulkOperateIdentities(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, filter IdentityFilter,
	req OperationRequest, limit int, dryRun bool) (BulkResult, error) {
	if s.pool == nil {
		return BulkResult{}, apperr.Internal(errNoPool)
	}
	if s.ops == nil && !dryRun {
		return BulkResult{}, apperr.Internal(errNoOperator)
	}
	if err := validateOperation(req, identityOperations, true); err != nil {
		return BulkResult{}, err
	}
	switch {
	case limit < 0 || limit > MaxBulkIdentities:
		return BulkResult{}, invalid("limit must be between 0 and %d", MaxBulkIdentities)
	case limit == 0:
		limit = MaxBulkIdentities
	}
	sites, err := s.filterSites(p, ns, &filter, authz.PermIdentityOperate)
	if err != nil {
		return BulkResult{}, err
	}
	if req.Scope == "identity_endpoint" {
		owner := groupSite(ns, req.EndpointGroupID)
		if owner == nil {
			return BulkResult{}, apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown, "endpoint group %q not found", truncateText(req.EndpointGroupID, 64))
		}
		sites = slices.DeleteFunc(sites, func(site *catalog.Site) bool { return site.ID != owner.ID })
	}
	ids, err := s.resolveFilterIDs(ctx, filter, sites, limit)
	if err != nil {
		return BulkResult{}, err
	}
	if dryRun {
		return BulkResult{Matched: len(ids), Failed: []BulkFailure{}}, nil
	}
	var result BulkResult
	var opErr error
	for start := 0; start < len(ids); start += OperateChunkSize {
		if err := ctx.Err(); err != nil {
			opErr = err
			break
		}
		res, err := s.ops.OperateIdentities(ctx, p, ns, ids[start:min(start+OperateChunkSize, len(ids))], req)
		if err != nil {
			opErr = err
			break
		}
		result.merge(res)
	}
	auditResult := audit.ResultOK
	if opErr != nil {
		auditResult = audit.ResultError
	}
	s.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, "identity.bulk_operate", "identity", "", "", auditResult,
		map[string]any{
			"operation": req.Operation, "scope": req.Scope, "endpoint_group_id": req.EndpointGroupID,
			"duration": req.Duration.String(), "site": filter.Site, "type": filter.Type, "states": filter.States,
			"resolved": len(ids), "matched": result.Matched, "succeeded": result.Succeeded, "failed": len(result.Failed),
		}))
	if opErr != nil {
		return BulkResult{}, opErr
	}
	return result, nil
}

// resolveFilterIDs returns up to limit IDs of identities matching filter on sites.
func (s *Service) resolveFilterIDs(ctx context.Context, filter IdentityFilter, sites []*catalog.Site, limit int) ([]string, error) {
	if len(sites) == 0 {
		return []string{}, nil
	}
	ids := make([]string, 0, min(limit, resolveBatchSize))
	siteList := siteIDs(sites)
	after := ""
	for len(ids) < limit {
		batch := min(resolveBatchSize, limit-len(ids))
		sql, args := identityIDsSQL(filter, siteList, after, batch)
		rows, err := s.pool.Query(ctx, sql, args...)
		if err != nil {
			return nil, fmt.Errorf("resolve identities: %w", err)
		}
		n := 0
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("resolve identities: %w", err)
			}
			ids = append(ids, id)
			after = id
			n++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("resolve identities: %w", err)
		}
		if n < batch {
			break
		}
	}
	return ids, nil
}

// RevertActions reverts automatic actions recorded in state events
// (identity:operate). An empty site covers every site the principal may
// operate: one Operator call without a site for unrestricted principals, one
// call per accessible site otherwise. At most MaxRevertIdentityIDs affected
// identity IDs are returned.
func (s *Service) RevertActions(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, in RevertInput) (BulkResult, []string, error) {
	if s.ops == nil {
		return BulkResult{}, nil, apperr.Internal(errNoOperator)
	}
	// Authorize before validating arguments so callers without permission
	// learn nothing about the request shape.
	visible, err := visibleSites(p, ns, authz.PermIdentityOperate)
	if err != nil {
		return BulkResult{}, nil, err
	}
	req := in.Request
	if err := validateRevert(&req, s.now()); err != nil {
		return BulkResult{}, nil, err
	}
	sites, err := narrowSites(p, ns, visible, in.Site, authz.PermIdentityOperate)
	if err != nil {
		return BulkResult{}, nil, err
	}
	targets := make([]string, 0, len(sites))
	if unrestricted, _ := p.SiteFilter(ns.TenantID, ns.ID, authz.PermIdentityOperate); unrestricted && in.Site == "" {
		targets = append(targets, "")
	} else {
		targets = append(targets, siteIDs(sites)...)
	}
	result := BulkResult{Failed: []BulkFailure{}}
	affected := []string{}
	seen := map[string]struct{}{}
	for _, siteID := range targets {
		r := req
		r.SiteID = siteID
		res, ids, err := s.ops.RevertActions(ctx, p, ns, r)
		if err != nil {
			return BulkResult{}, nil, err
		}
		result.merge(res)
		for _, id := range ids {
			if len(affected) == MaxRevertIdentityIDs {
				break
			}
			if _, dup := seen[id]; dup || id == "" {
				continue
			}
			seen[id] = struct{}{}
			affected = append(affected, id)
		}
	}
	return result, affected, nil
}

// validateRevert checks and normalizes a revert request: From is required, a
// zero To means now, and empty Actions means every revertible action.
func validateRevert(req *RevertRequest, now time.Time) error {
	if req.From.IsZero() {
		return invalid("time_range.start is required")
	}
	if req.To.IsZero() {
		req.To = now.UTC()
	}
	if !req.From.Before(req.To) {
		return invalid("time_range.start must be before time_range.end")
	}
	if len(req.PolicyID) > 64 {
		return invalid("policy_id must be at most 64 bytes")
	}
	if err := validateText("rule", req.Rule, 128); err != nil {
		return err
	}
	if len(req.Actions) == 0 {
		req.Actions = slices.Clone(revertActions)
		return nil
	}
	for _, a := range req.Actions {
		if !slices.Contains(revertActions, a) {
			return invalid("action %q must be one of ban, quarantine, expire, cooldown", truncateText(a, 32))
		}
	}
	req.Actions = dedupe(req.Actions)
	return nil
}
