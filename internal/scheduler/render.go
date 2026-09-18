package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/identity"
)

// renderParallelism bounds concurrent credential/proxy rendering of a batch.
const renderParallelism = 8

var (
	errNoCredentialSource = errors.New("no credential source configured")
	errNoProxyResolver    = errors.New("no proxy resolver configured")
)

// render turns issued leases into grants (spec §6.1 step 5). Leases whose
// credential or proxy cannot be rendered are released; the call fails with
// internal when no lease survives. When ctx is canceled every lease is
// released so that abandoned requests do not hold identities.
func (s *Service) render(ctx context.Context, t *target, leases []scriptLease) ([]*Grant, error) {
	grants := make([]*Grant, len(leases))
	errs := make([]error, len(leases))
	if len(leases) == 1 {
		grants[0], errs[0] = s.renderOne(ctx, t, leases[0])
	} else {
		var wg sync.WaitGroup
		sem := make(chan struct{}, renderParallelism)
		for i := range leases {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				grants[i], errs[i] = s.renderOne(ctx, t, leases[i])
			}(i)
		}
		wg.Wait()
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		for _, l := range leases {
			s.releaseDetached(ctx, t, l.ID)
		}
		return nil, ctxErr
	}
	out := make([]*Grant, 0, len(leases))
	var firstErr error
	for i, l := range leases {
		if errs[i] != nil {
			s.logRenderFailure(t, l, errs[i])
			s.releaseDetached(ctx, t, l.ID)
			if firstErr == nil {
				firstErr = errs[i]
			}
			continue
		}
		out = append(out, grants[i])
	}
	if len(out) == 0 {
		return nil, apperr.Internal(firstErr)
	}
	return out, nil
}

// renderOne renders the credential and proxy of one lease.
func (s *Service) renderOne(ctx context.Context, t *target, l scriptLease) (*Grant, error) {
	typ := t.site.IdentityTypes[l.TypeName]
	if typ == nil {
		return nil, fmt.Errorf("identity type %q of identity %s is not in the catalog", l.TypeName, l.IdentityID)
	}
	if s.creds == nil {
		return nil, errNoCredentialSource
	}
	cred, err := s.creds.Credential(ctx, typ, t.ns.ID, l.IdentityID, l.PayloadVersion)
	if err != nil {
		return nil, fmt.Errorf("render credential of identity %s: %w", l.IdentityID, err)
	}
	if cred == nil {
		cred = &identity.Credential{}
	}
	var px *ProxyAssignment
	if l.ProxyID != "" {
		if s.proxies == nil {
			return nil, errNoProxyResolver
		}
		px, err = s.proxies.Resolve(ctx, t.ns.ID, l.ProxyID, l.IdentityID, l.ID)
		if err != nil {
			return nil, fmt.Errorf("resolve proxy %s: %w", l.ProxyID, err)
		}
		if px == nil {
			return nil, fmt.Errorf("resolve proxy %s: no assignment", l.ProxyID)
		}
	}
	return &Grant{
		Lease: Lease{
			ID:            l.ID,
			IdentityID:    l.IdentityID,
			IdentityType:  l.TypeName,
			EndpointGroup: t.group.Name,
			ExpiresAt:     time.UnixMilli(l.ExpiresMs),
			Sticky:        l.Sticky,
			Probe:         l.Probe || l.State == "pending",
		},
		Credential:   cred,
		Proxy:        px,
		RenewBefore:  t.rot.Rotation.LeaseTTL.Std() / 4,
		breakerProbe: l.Probe,
	}, nil
}

// releaseDetached releases a lease that was never delivered (rendering failed
// or the request was canceled), even when ctx is canceled. The release aborts
// the lease so that it does not consume the identity's reuse interval, quota
// or a half-open probe slot.
func (s *Service) releaseDetached(ctx context.Context, t *target, leaseID string) {
	opts := releaseOpts{detach: true, abort: true, quotaWindows: t.rot.Rotation.Quota}
	if _, err := s.release(ctx, t.site.Key, t.site, leaseID, s.clock(), opts); err != nil {
		s.logger.Error("release after failed acquire",
			"site_id", t.site.ID, "lease_id", leaseID, "error", err)
	}
}
