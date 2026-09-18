package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/pkg/admit"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/site"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

const (
	// sitePausedRetryMs is the retry hint of site_paused errors.
	sitePausedRetryMs = 30000
)

// Admission decision labels of spinneret_acquire_admission_total. The two shed
// causes are kept apart on purpose: with wait_ms = 0 — the default of every
// client — a caller is never parked, so shedding it says only "this instance was
// at its limit", while a full wait room says "the callers that did offer to wait
// could not be parked either", which is a much deeper overload.
const (
	admissionImmediate     = "immediate"
	admissionQueued        = "queued"
	admissionShedNoWait    = "shed_no_wait"
	admissionShedQueueFull = "shed_queue_full"
	admissionShedTimeout   = "shed_timeout"
	admissionShedCanceled  = "shed_canceled"
)

// waitDelays is the retry schedule of wait_ms (spec §6.1 step 4); the last
// value repeats.
var waitDelays = [...]time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 150 * time.Millisecond, 200 * time.Millisecond}

// target is the resolved destination of an acquire request.
type target struct {
	ns     *catalog.Namespace
	site   *catalog.Site
	group  *catalog.EndpointGroup
	rot    *policy.RotationSpec
	client string
}

func (t *target) groupID() string {
	if t.group == nil {
		return ""
	}
	return t.group.ID
}

func (t *target) groupName() string {
	if t.group == nil {
		return ""
	}
	return t.group.Name
}

// Acquire leases one identity (spec §6.1). The principal must be an API token
// holding lease:acquire on the site.
func (s *Service) Acquire(ctx context.Context, req AcquireRequest) (*Grant, error) {
	req.Count = 1
	grants, err := s.acquire(ctx, req)
	if err != nil {
		return nil, err
	}
	return grants[0], nil
}

// AcquireBatch leases up to req.Count distinct identities. It returns the
// leases that could be issued and fails only when none could be issued.
// Half-open breakers issue a single probe lease.
func (s *Service) AcquireBatch(ctx context.Context, req AcquireRequest) ([]*Grant, error) {
	if req.Count < 1 || req.Count > MaxBatch {
		return nil, apperr.InvalidArgument("", "count must be between 1 and %d", MaxBatch)
	}
	return s.acquire(ctx, req)
}

func (s *Service) acquire(ctx context.Context, req AcquireRequest) ([]*Grant, error) {
	began := time.Now()
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Wait < 0 || req.Wait > MaxWait {
		return nil, apperr.InvalidArgument("", "wait_ms must be between 0 and %d", MaxWait.Milliseconds())
	}
	t, err := s.resolveSite(p, req)
	if err != nil {
		return nil, err
	}
	if t.site.Paused {
		s.finish(p, t, ResultSitePaused, nil, began)
		return nil, apperr.Unavailable(apperr.ReasonSitePaused, sitePausedRetryMs, "site %q is paused", t.site.Name)
	}
	if err := resolveGroup(t, req); err != nil {
		return nil, err
	}
	session := ""
	if req.SessionKey != "" {
		session = redis.NormalizeSessionKey(req.SessionKey)
	}

	out, err := s.acquireWithWait(ctx, p, t, req.Count, session, req.Wait)
	if err != nil {
		s.finish(p, t, resultOf(out, err), nil, began)
		return nil, err
	}
	s.notifyBindings(t, out.Leases)
	grants, err := s.render(ctx, t, out.Leases)
	if err != nil {
		s.finish(p, t, ResultError, nil, began)
		return nil, err
	}
	s.finish(p, t, ResultOK, grants, began)
	return grants, nil
}

// resolveSite resolves the namespace, site and client of a request and
// checks lease:acquire (spec §6.1 steps 1-2).
func (s *Service) resolveSite(p *authz.Principal, req AcquireRequest) (*target, error) {
	ns, err := s.principalNamespace(p)
	if err != nil {
		return nil, err
	}
	if req.Site == "" {
		return nil, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site is required")
	}
	st, ok := ns.Sites[req.Site]
	if !ok {
		return nil, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site %q not found", req.Site)
	}
	if !st.HasClient(req.Client) {
		return nil, apperr.InvalidArgument(apperr.ReasonClientUnknown, "client %q is not declared by site %q", req.Client, st.Name)
	}
	if err := p.Require(authz.PermLeaseAcquire, siteResource(ns, st)); err != nil {
		return nil, err
	}
	return &target{ns: ns, site: st, client: req.Client}, nil
}

// resolveGroup resolves the endpoint group (spec §6.1 step 3).
func resolveGroup(t *target, req AcquireRequest) error {
	var (
		g  *catalog.EndpointGroup
		ok bool
	)
	switch {
	case req.EndpointGroup != "":
		g, ok = t.site.Group(t.client, req.EndpointGroup)
		if !ok {
			return apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown, "endpoint group %q not found for %s/%s", req.EndpointGroup, t.site.Name, t.client)
		}
	case req.URI != "":
		if len(req.URI) > site.MaxPathLength {
			return apperr.InvalidArgument(apperr.ReasonURIInvalid, "uri must be at most %d bytes", site.MaxPathLength)
		}
		path, err := site.NormalizePath(req.URI)
		if err != nil {
			return err
		}
		g, _, ok = t.site.MatchGroup(t.client, path)
	default:
		g, ok = t.site.Group(t.client, site.DefaultGroup)
	}
	if !ok || g == nil {
		return apperr.Internal(fmt.Errorf("site %s client %s has no default endpoint group", t.site.ID, t.client))
	}
	t.group = g
	t.rot = rotationOf(g)
	return nil
}

// acquireWithWait runs acquire.lua, retrying until the wait budget is spent
// (spec §6.1 step 4). Attempts shed by admission
// control never reach Redis; they consume a rung of the ladder and sleep
// outside the gate, which is how load above the knee queues in the server
// instead of multiplying concurrency at Redis.
func (s *Service) acquireWithWait(ctx context.Context, p *authz.Principal, t *target, count int, session string, wait time.Duration) (acquireOutcome, error) {
	deadline := time.Now().Add(wait)
	var (
		out     acquireOutcome
		shed    error // the last shed error, when the last attempt was shed
		reached bool  // a previous attempt got a reply from Redis
	)
	for attempt := 0; ; attempt++ {
		o, waited, err := s.execAcquire(ctx, p, t, count, session, time.Until(deadline))
		switch {
		case apperr.ReasonOf(err) == apperr.ReasonOverloaded:
			shed = err
		case err != nil:
			// A failed attempt reports no outcome, exactly as it did before
			// admission control existed: out is kept across attempts only to
			// decide the exhausted/shed reason, and letting an earlier attempt's
			// EXHAUSTED status escape here would reclassify the recorded
			// statistics result of a cancelled request from error to exhausted.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return acquireOutcome{}, ctxErr
			}
			return acquireOutcome{}, apperr.Internal(fmt.Errorf("acquire %s/%s: %w", t.site.ID, t.group.ID, err))
		default:
			shed, reached, out = nil, true, o
			if out.Transition && s.cfg.OnBreakerHalfOpen != nil {
				s.cfg.OnBreakerHalfOpen(t.site.Key, t.group.Key)
			}
			switch out.Status {
			case statusOK:
				return out, nil
			case statusBreakerOpen:
				return out, apperr.Unavailable(apperr.ReasonCircuitOpen, max(out.RetryAfterMs, 1),
					"circuit breaker of %s/%s/%s is open", t.site.Name, t.client, t.group.Name)
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			// A shed only reports overloaded when no attempt reached Redis:
			// once the script has said EXHAUSTED or NO_PROXY, the caller's real
			// problem is the pool and the existing reason must survive.
			if shed != nil && !reached {
				return out, shed
			}
			return out, exhaustedError(t, out)
		}
		delay := ladderDelay(attempt, waited, remaining)
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return out, ctx.Err()
		case <-timer.C:
		}
	}
}

// ladderDelay returns how long to sleep before retry attempt+1. The time the
// attempt already spent waiting for an admission permit is credited against its
// rung, so a given wait_ms buys the same number of attempts with admission
// control as without it; the sleep never overruns the remaining budget, and a
// fully credited rung returns zero or less, which retries immediately.
func ladderDelay(attempt int, waited, remaining time.Duration) time.Duration {
	return min(waitDelays[min(attempt, len(waitDelays)-1)]-waited, remaining)
}

func exhaustedError(t *target, out acquireOutcome) error {
	if out.Status == statusNoProxy {
		return apperr.ResourceExhausted(apperr.ReasonNoProxyAvailable, clampRetry(out.RetryAfterMs),
			"no proxy available for %s/%s/%s", t.site.Name, t.client, t.group.Name)
	}
	return apperr.ResourceExhausted(apperr.ReasonNoIdentityAvailable, clampRetry(out.RetryAfterMs),
		"no identity available for %s/%s/%s", t.site.Name, t.client, t.group.Name)
}

// overloadedError reports that this instance is at its acquire concurrency
// limit. The request issued no Redis command, so it is safe to retry; the hint
// is jittered into [overloadRetryBaseMs, 2*overloadRetryBaseMs) because clients
// retry unavailable automatically and an unjittered hint would return the whole
// shed population in lockstep.
func (s *Service) overloadedError(t *target, cause error) error {
	ms := clampRetry(overloadRetryBaseMs + int64(float64(overloadRetryBaseMs)*s.random()))
	return apperr.Unavailable(apperr.ReasonOverloaded, ms,
		"server is at its acquire concurrency limit for %s/%s/%s",
		t.site.Name, t.client, t.group.Name).WithCause(cause)
}

// resultOf maps a failed acquire to its statistics result.
func resultOf(out acquireOutcome, err error) string {
	switch apperr.ReasonOf(err) {
	case apperr.ReasonOverloaded:
		return ResultOverloaded
	case apperr.ReasonNoIdentityAvailable:
		return ResultExhausted
	case apperr.ReasonNoProxyAvailable:
		return ResultNoProxy
	case apperr.ReasonCircuitOpen:
		return ResultCircuitOpen
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		switch out.Status {
		case statusExhausted:
			return ResultExhausted
		case statusNoProxy:
			return ResultNoProxy
		}
	}
	return ResultError
}

// execAcquire performs one acquire.lua call under admission control. budget is
// the caller's remaining wait_ms; it bounds how long the call may wait for a
// permit. The returned duration is the time spent waiting for that permit,
// which the caller credits against its retry delay.
func (s *Service) execAcquire(ctx context.Context, p *authz.Principal, t *target, count int, session string, budget time.Duration) (acquireOutcome, time.Duration, error) {
	// The permit is taken before any CPU work so a shed costs no allocation and
	// no draw from s.random, and it is held across the script call only — never
	// across the wait ladder's sleeps, so one permit represents exactly one unit
	// of Redis concurrency.
	//
	// The permit is weighted by count: a batch does roughly count times the Lua
	// work of a single acquire and must not be charged as one.
	permit, adm, err := s.admit.Acquire(ctx, count, budget)
	defer permit.Release()
	switch {
	// ErrNoBudget wraps ErrQueueFull, so it has to be tested first.
	case errors.Is(err, admit.ErrNoBudget):
		s.observeAdmission(admissionShedNoWait, adm.Waited)
		return acquireOutcome{}, adm.Waited, s.overloadedError(t, err)
	case errors.Is(err, admit.ErrQueueFull):
		s.observeAdmission(admissionShedQueueFull, adm.Waited)
		return acquireOutcome{}, adm.Waited, s.overloadedError(t, err)
	case errors.Is(err, admit.ErrQueueTimeout):
		s.observeAdmission(admissionShedTimeout, adm.Waited)
		return acquireOutcome{}, adm.Waited, s.overloadedError(t, err)
	case err != nil: // context cancellation or deadline
		s.observeAdmission(admissionShedCanceled, adm.Waited)
		return acquireOutcome{}, adm.Waited, err
	}
	if adm.Queued {
		s.observeAdmission(admissionQueued, adm.Waited)
	} else {
		s.observeAdmission(admissionImmediate, 0)
	}

	call := acquireCall{
		now:      s.clock(),
		count:    count,
		session:  session,
		node:     p.Node,
		tokenID:  p.ID,
		prefixes: make([]string, count),
		randoms:  make([]float64, count*randomsPerLease+randomsExtra),
	}
	for i := range call.prefixes {
		call.prefixes[i] = idgen.LeasePrefix(t.site.Key)
	}
	for i := range call.randoms {
		call.randoms[i] = s.random()
	}
	args := s.acquireArgs(t.ns.ID, t.group, t.rot, call)
	cctx, cancel := context.WithTimeout(ctx, redisTimeout)
	defer cancel()
	// The script histogram measures the wall-clock round trip, so it uses
	// time.Now rather than the injectable s.clock, which tests freeze.
	began := time.Now()
	vals, err := replyStrings(acquireScript.Exec(cctx, s.rdb, []string{s.keys.SiteMeta(t.site.Key)}, args))
	s.observeAcquireScript(time.Since(began))
	if err != nil {
		return acquireOutcome{}, adm.Waited, fmt.Errorf("run acquire script: %w", err)
	}
	out, perr := parseAcquire(vals)
	return out, adm.Waited, perr
}

// notifyBindings reports proxy bindings created by acquire.
func (s *Service) notifyBindings(t *target, leases []scriptLease) {
	if s.cfg.OnProxyBound == nil {
		return
	}
	for _, l := range leases {
		if !l.Bound {
			continue
		}
		s.cfg.OnProxyBound(ProxyBinding{
			At:           s.clock(),
			NamespaceID:  t.ns.ID,
			SiteID:       t.site.ID,
			SiteKey:      t.site.Key,
			IdentityID:   l.IdentityID,
			IdentityKey:  l.IdentityKey,
			ProxyID:      l.ProxyID,
			ProxyKey:     l.ProxyKey,
			Rebound:      l.Rebound,
			RebindDay:    s.clock().UTC().Format(rebindDayLayout),
			RebindsToday: l.RebindsToday,
		})
	}
}

// finish records metrics and statistics of an acquire call.
func (s *Service) finish(p *authz.Principal, t *target, result string, grants []*Grant, began time.Time) {
	d := time.Since(began)
	s.observeAcquire(t.site.Name, t.groupName(), result, d)
	rec := AcquireRecord{
		At:              began,
		TenantID:        t.ns.TenantID,
		NamespaceID:     t.ns.ID,
		SiteID:          t.site.ID,
		Site:            t.site.Name,
		EndpointGroupID: t.groupID(),
		EndpointGroup:   t.groupName(),
		Client:          t.client,
		Node:            p.Node,
		TokenID:         p.ID,
		Result:          result,
		Duration:        d,
	}
	if len(grants) == 0 {
		s.rec.RecordAcquire(rec)
		return
	}
	for _, g := range grants {
		r := rec
		r.IdentityID = g.Lease.IdentityID
		if typ := t.site.IdentityTypes[g.Lease.IdentityType]; typ != nil {
			r.IdentityTypeID = typ.ID
		}
		if g.Proxy != nil {
			r.ProxyID = g.Proxy.ID
		}
		r.LeaseID = g.Lease.ID
		r.Probe = g.breakerProbe
		r.Sticky = g.Lease.Sticky
		s.rec.RecordAcquire(r)
	}
}

// logRenderFailure logs a credential or proxy rendering failure without
// payload or proxy details.
func (s *Service) logRenderFailure(t *target, l scriptLease, err error) {
	s.logger.Error("lease rendering failed; lease released",
		slog.String("site_id", t.site.ID),
		slog.String("endpoint_group_id", t.group.ID),
		slog.String("identity_id", l.IdentityID),
		slog.String("lease_id", l.ID),
		slog.Any("error", err))
}
