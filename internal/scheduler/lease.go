package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/policy"
)

// releaseResult is the decoded reply of release.lua.
type releaseResult struct {
	Status      string
	IdentityKey int64
	GroupKey    int64
	ProxyKey    int64
	ProxyID     string
	IdentityID  string
	Node        string
	TokenID     string
	Probe       bool
}

// Renew extends an active lease of the caller's namespace by extend (0 = the
// lease TTL), bounded by the lease lifetime cap. It returns the new expiry.
func (s *Service) Renew(ctx context.Context, leaseID string, extend time.Duration) (time.Time, error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if extend < 0 || extend > MaxRenewExtension {
		return time.Time{}, apperr.InvalidArgument("", "extend_ms must be between 0 and %d", MaxRenewExtension.Milliseconds())
	}
	ns, st, err := s.leaseSite(p, leaseID)
	if err != nil {
		return time.Time{}, err
	}
	now := s.clock()
	args := []string{itoa(now.UnixMilli()), leaseID, itoa(extend.Milliseconds()), ns.ID}
	cctx, cancel := scriptContext(ctx, false)
	defer cancel()
	vals, err := replyStrings(renewScript.Exec(cctx, s.rdb, []string{s.keys.SiteMeta(st.Key)}, args))
	if err == nil && len(vals) == 0 {
		err = errEmptyReply
	}
	if err != nil {
		return time.Time{}, apperr.Internal(fmt.Errorf("renew lease %s: %w", leaseID, err))
	}
	switch vals[0] {
	case statusOK:
		if len(vals) < 8 {
			return time.Time{}, apperr.Internal(fmt.Errorf("renew lease %s: short reply", leaseID))
		}
		expires := time.UnixMilli(atoi64(vals[1]))
		s.recordLeaseEnd(ns, st, EndRenewed, now, leaseID, atoi64(vals[3]), vals[2], vals[4], vals[5], vals[6], vals[7] == "1")
		return expires, nil
	case statusUnknown:
		return time.Time{}, leaseUnknown()
	case statusReleased:
		return time.Time{}, apperr.FailedPrecondition(apperr.ReasonLeaseReleased, "lease has been released")
	case statusExpired:
		return time.Time{}, apperr.FailedPrecondition(apperr.ReasonLeaseExpired, "lease has expired")
	case statusLifetimeExceeded:
		return time.Time{}, apperr.FailedPrecondition(apperr.ReasonLeaseLifetimeExceeded, "lease reached its maximum lifetime")
	default:
		return time.Time{}, apperr.Internal(fmt.Errorf("renew lease %s: unexpected status %q", leaseID, vals[0]))
	}
}

// Release ends a lease of the caller's namespace. It returns false when the
// lease had already been released or had expired (the call is idempotent).
func (s *Service) Release(ctx context.Context, leaseID string) (bool, error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return false, err
	}
	ns, st, err := s.leaseSite(p, leaseID)
	if err != nil {
		return false, err
	}
	now := s.clock()
	res, err := s.release(ctx, st.Key, st, leaseID, now, releaseOpts{expectNS: ns.ID})
	if err != nil {
		return false, err
	}
	switch res.Status {
	case statusReleased:
		s.recordReleased(ns, st, now, leaseID, res)
		return true, nil
	case statusEnded:
		return false, nil
	default:
		return false, leaseUnknown()
	}
}

// ReleaseLease ends a lease on behalf of the report worker (a report with
// release=true). It returns false when the lease is unknown or already ended.
func (s *Service) ReleaseLease(ctx context.Context, siteKey int64, leaseID string, now time.Time) (bool, error) {
	st, ns, _ := s.cat.SiteByKey(siteKey)
	res, err := s.release(ctx, siteKey, st, leaseID, now, releaseOpts{})
	if err != nil {
		return false, err
	}
	if res.Status != statusReleased {
		return false, nil
	}
	s.recordReleased(ns, st, now, leaseID, res)
	return true, nil
}

// leaseSite authorizes access to a lease: it must belong to a site of the
// token's namespace on which the token holds lease:acquire. Every failure
// other than a non-token principal is reported as lease_unknown so that
// lease IDs of other namespaces or sites are not revealed.
func (s *Service) leaseSite(p *authz.Principal, leaseID string) (*catalog.Namespace, *catalog.Site, error) {
	ns, err := s.principalNamespace(p)
	if err != nil {
		return nil, nil, err
	}
	ref, err := idgen.ParseLeaseID(leaseID)
	if err != nil {
		return nil, nil, leaseUnknown()
	}
	st, siteNS, ok := s.cat.SiteByKey(ref.SiteKey)
	if !ok || siteNS.ID != ns.ID {
		return nil, nil, leaseUnknown()
	}
	if !p.Can(authz.PermLeaseAcquire, siteResource(ns, st)) {
		return nil, nil, leaseUnknown()
	}
	return ns, st, nil
}

// releaseOpts selects how release.lua ends a lease.
type releaseOpts struct {
	// expectNS restricts the lease to a namespace ("" = any).
	expectNS string
	// detach completes the call even when ctx is canceled.
	detach bool
	// abort ends a lease that was never delivered to the caller (rendering
	// failed or the request was canceled): instead of applying the released
	// reuse anchor, the reuse value, quota count and half-open probe slot
	// charged by acquire are rolled back.
	abort bool
	// quotaWindows are the quota windows of the lease's rotation policy
	// (used when aborting).
	quotaWindows []policy.QuotaSpec
}

// release runs release.lua.
func (s *Service) release(ctx context.Context, siteKey int64, st *catalog.Site, leaseID string, now time.Time, opts releaseOpts) (releaseResult, error) {
	layout := s.layouts.layout(st)
	args := make([]string, 0, 6+len(opts.quotaWindows)+len(layout))
	args = append(args, itoa(now.UnixMilli()), leaseID, itoa(s.cfg.LateReportWindow.Milliseconds()), opts.expectNS)
	if opts.abort {
		args = append(args, "1", strconv.Itoa(len(opts.quotaWindows)))
		for _, q := range opts.quotaWindows {
			args = append(args, itoa(q.Window.Milliseconds()))
		}
	} else {
		args = append(args, "0", "0")
	}
	args = append(args, layout...)
	cctx, cancel := scriptContext(ctx, opts.detach)
	defer cancel()
	vals, err := replyStrings(releaseScript.Exec(cctx, s.rdb, []string{s.keys.SiteMeta(siteKey)}, args))
	if err == nil && len(vals) == 0 {
		err = errEmptyReply
	}
	if err != nil {
		return releaseResult{}, apperr.Internal(fmt.Errorf("release lease %s: %w", leaseID, err))
	}
	res := releaseResult{Status: vals[0]}
	switch res.Status {
	case statusReleased:
		if len(vals) < 9 {
			return releaseResult{}, apperr.Internal(fmt.Errorf("release lease %s: short reply", leaseID))
		}
		res.IdentityKey = atoi64(vals[1])
		res.GroupKey = atoi64(vals[2])
		res.ProxyKey = atoi64(vals[3])
		res.ProxyID = vals[4]
		res.IdentityID = vals[5]
		res.Node = vals[6]
		res.TokenID = vals[7]
		res.Probe = vals[8] == "1"
	case statusEnded, statusUnknown:
	default:
		return releaseResult{}, apperr.Internal(fmt.Errorf("release lease %s: unexpected status %q", leaseID, res.Status))
	}
	return res, nil
}

func (s *Service) recordReleased(ns *catalog.Namespace, st *catalog.Site, now time.Time, leaseID string, res releaseResult) {
	s.recordLeaseEnd(ns, st, EndReleased, now, leaseID, res.GroupKey, res.IdentityID, res.ProxyID, res.Node, res.TokenID, res.Probe)
}

// recordLeaseEnd records a lease-end statistic. ns and st may be nil when the
// site is no longer in the catalog.
func (s *Service) recordLeaseEnd(ns *catalog.Namespace, st *catalog.Site, kind string, at time.Time, leaseID string, groupKey int64, identityID, proxyID, node, tokenID string, probe bool) {
	rec := LeaseEndRecord{
		At:         at,
		IdentityID: identityID,
		ProxyID:    proxyID,
		LeaseID:    leaseID,
		Node:       node,
		TokenID:    tokenID,
		Kind:       kind,
		Probe:      probe,
	}
	if ns != nil {
		rec.TenantID = ns.TenantID
		rec.NamespaceID = ns.ID
	}
	if st != nil {
		rec.SiteID = st.ID
		rec.Site = st.Name
		if g := st.GroupsByKey[groupKey]; g != nil {
			rec.EndpointGroupID = g.ID
			rec.EndpointGroup = g.Name
			rec.Client = g.Client
		}
	}
	s.rec.RecordLeaseEnd(rec)
}
