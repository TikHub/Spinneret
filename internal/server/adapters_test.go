package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/action"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/hotstate"
	"github.com/TikHub/Spinneret/internal/identitysvc"
	"github.com/TikHub/Spinneret/internal/pkg/durationx"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/proxy"
	"github.com/TikHub/Spinneret/internal/scheduler"
	"github.com/TikHub/Spinneret/internal/stats"
	"github.com/TikHub/Spinneret/internal/vault"
	"github.com/TikHub/Spinneret/internal/worker"
)

// fakeHot records syncer calls and returns canned hot state.
type fakeHot struct {
	syncOpts  hotstate.SyncOptions
	calls     []string
	hot       hotstate.IdentityHot
	hotErr    error
	lastSite  string
	lastIDs   []string
	lastNSID  string
	returnErr error
}

func (f *fakeHot) SyncIdentities(_ context.Context, siteID string, ids []string, opts hotstate.SyncOptions) error {
	f.calls, f.lastSite, f.lastIDs, f.syncOpts = append(f.calls, "sync_identities"), siteID, ids, opts
	return f.returnErr
}

func (f *fakeHot) RemoveIdentities(_ context.Context, siteID string, ids []string) error {
	f.calls, f.lastSite, f.lastIDs = append(f.calls, "remove_identities"), siteID, ids
	return f.returnErr
}

func (f *fakeHot) SyncAccounts(_ context.Context, siteID string, ids []string) error {
	f.calls, f.lastSite, f.lastIDs = append(f.calls, "sync_accounts"), siteID, ids
	return f.returnErr
}

func (f *fakeHot) SyncProxies(_ context.Context, nsID string, ids []string) error {
	f.calls, f.lastNSID, f.lastIDs = append(f.calls, "sync_proxies"), nsID, ids
	return f.returnErr
}

func (f *fakeHot) IdentityHotState(context.Context, *catalog.Site, string) (hotstate.IdentityHot, error) {
	return f.hot, f.hotErr
}

func TestHotSyncerAdaptersConvertOptionsAndPassThrough(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	hot := &fakeHot{returnErr: boom}

	ih := identityHotSyncer{hot: hot}
	require.ErrorIs(t, ih.SyncIdentities(ctx, "sit_1", []string{"idt_1"}, identitysvc.SyncOptions{ResetHealth: true}), boom)
	require.Equal(t, hotstate.SyncOptions{ResetHealth: true}, hot.syncOpts)
	require.ErrorIs(t, ih.RemoveIdentities(ctx, "sit_2", []string{"idt_2"}), boom)
	require.Equal(t, "sit_2", hot.lastSite)
	require.ErrorIs(t, ih.SyncAccounts(ctx, "sit_3", []string{"acc_1"}), boom)
	require.Equal(t, []string{"acc_1"}, hot.lastIDs)

	ah := actionHotSyncer{hot: hot}
	require.ErrorIs(t, ah.SyncIdentities(ctx, "sit_1", nil, action.SyncOptions{ResetFailures: true}), boom)
	require.Equal(t, hotstate.SyncOptions{ResetFailures: true}, hot.syncOpts)
	require.ErrorIs(t, ah.SyncAccounts(ctx, "sit_4", nil), boom)
	require.ErrorIs(t, ah.SyncProxies(ctx, "ns_1", []string{"pxy_1"}), boom)
	require.Equal(t, "ns_1", hot.lastNSID)
	require.Equal(t, []string{"sync_identities", "remove_identities", "sync_accounts", "sync_identities", "sync_accounts", "sync_proxies"}, hot.calls)
}

func TestIdentityHotReaderCopiesEveryField(t *testing.T) {
	at := func(s int) time.Time { return time.Unix(1_700_000_000+int64(s), 0).UTC() }
	hot := &fakeHot{hot: hotstate.IdentityHot{
		Present: true, State: "active", ActiveLeases: 2,
		SiteCooldownUntil: at(1), SiteReuseUntil: at(2), ExclusiveUntil: at(3),
		BoundProxyID: "pxy_1", GlobalScore: 71.5, GlobalSamples: 9, AccountCooldownUntil: at(4),
		Groups: []hotstate.EndpointHot{{
			EndpointGroupID: "eg_1", EndpointGroup: "search", Client: "web", Score: 42.25, Samples: 7, ConsecutiveFailures: 3,
			CooldownUntil: at(5), ReuseUntil: at(6), LastUsedAt: at(7), AvailableAt: at(8), InReadyQueue: true,
		}},
	}}
	got, err := identityHotReader{hot: hot}.IdentityHotState(context.Background(), &catalog.Site{}, "idt_1")
	require.NoError(t, err)
	require.Equal(t, identitysvc.HotState{
		Present: true, State: "active", ActiveLeases: 2,
		SiteCooldownUntil: at(1), SiteReuseUntil: at(2), ExclusiveUntil: at(3),
		BoundProxyID: "pxy_1", GlobalScore: 71.5, GlobalSamples: 9, AccountCooldownUntil: at(4),
		Groups: []identitysvc.EndpointHotState{{
			EndpointGroup: "search", EndpointGroupID: "eg_1", Client: "web", Score: 42.25, Samples: 7, ConsecutiveFailures: 3,
			CooldownUntil: at(5), ReuseUntil: at(6), LastUsedAt: at(7), AvailableAt: at(8), InReadyQueue: true,
		}},
	}, got)

	// Application errors pass through unchanged.
	hot.hotErr = apperr.NotFound("identity not found")
	_, err = identityHotReader{hot: hot}.IdentityHotState(context.Background(), &catalog.Site{}, "idt_x")
	require.True(t, apperr.IsNotFound(err))

	require.Nil(t, convertIdentityHot(hotstate.IdentityHot{}).Groups)
}

// fakeOperator records requests and returns canned results.
type fakeOperator struct {
	req    action.OperationRequest
	revert action.RevertRequest
	res    action.BulkResult
	err    error
}

func (f *fakeOperator) OperateIdentities(_ context.Context, _ *authz.Principal, _ *catalog.Namespace, _ []string, req action.OperationRequest) (action.BulkResult, error) {
	f.req = req
	return f.res, f.err
}

func (f *fakeOperator) OperateAccount(_ context.Context, _ *authz.Principal, _ *catalog.Namespace, _ string, req action.OperationRequest) (action.BulkResult, error) {
	f.req = req
	return f.res, f.err
}

func (f *fakeOperator) RevertActions(_ context.Context, _ *authz.Principal, _ *catalog.Namespace, req action.RevertRequest) (action.BulkResult, []string, error) {
	f.revert = req
	return f.res, []string{"idt_1"}, f.err
}

func TestIdentityOperatorConvertsRequestsAndResults(t *testing.T) {
	ctx := context.Background()
	op := &fakeOperator{res: action.BulkResult{Matched: 3, Succeeded: 2, Failed: []action.BulkFailure{{ID: "idt_9", Reason: "not_found", Message: "gone"}}}}
	a := identityOperator{op: op}
	req := identitysvc.OperationRequest{
		Operation: "cooldown", Scope: "identity_endpoint", EndpointGroupID: "eg_1",
		Duration: durationx.MustParse("10m"), Reason: "manual", ResetFailures: true, ResetHealth: true,
	}
	want := identitysvc.BulkResult{Matched: 3, Succeeded: 2, Failed: []identitysvc.BulkFailure{{ID: "idt_9", Reason: "not_found", Message: "gone"}}}

	res, err := a.OperateIdentities(ctx, nil, nil, []string{"idt_1"}, req)
	require.NoError(t, err)
	require.Equal(t, want, res)
	require.Equal(t, action.OperationRequest{
		Operation: "cooldown", Scope: "identity_endpoint", EndpointGroupID: "eg_1",
		Duration: durationx.MustParse("10m"), Reason: "manual", ResetFailures: true, ResetHealth: true,
	}, op.req)

	op.err = apperr.PermissionDenied(apperr.ReasonPermissionDenied, "no")
	res, err = a.OperateAccount(ctx, nil, nil, "acc_1", req)
	require.Error(t, err)
	require.Equal(t, want, res)

	op.err = nil
	from, to := time.Unix(10, 0), time.Unix(20, 0)
	res, ids, err := a.RevertActions(ctx, nil, nil, identitysvc.RevertRequest{
		SiteID: "sit_1", PolicyID: "pol_1", Rule: "r", Actions: []string{"ban"}, From: from, To: to, ResetFailures: true, ResetHealth: true, DryRun: true,
	})
	require.NoError(t, err)
	require.Equal(t, want, res)
	require.Equal(t, []string{"idt_1"}, ids)
	require.Equal(t, action.RevertRequest{
		SiteID: "sit_1", PolicyID: "pol_1", Rule: "r", Actions: []string{"ban"}, From: from, To: to, ResetFailures: true, ResetHealth: true, DryRun: true,
	}, op.revert)

	require.Nil(t, convertBulkResult(action.BulkResult{Matched: 1}).Failed)
}

type fakeResolver struct {
	asg *proxy.Assignment
	err error
}

func (f fakeResolver) Resolve(context.Context, string, string, string, string) (*proxy.Assignment, error) {
	return f.asg, f.err
}

func TestSchedulerProxyResolver(t *testing.T) {
	ctx := context.Background()
	got, err := schedulerProxyResolver{r: fakeResolver{asg: &proxy.Assignment{ID: "pxy_1", URL: "http://u:p@h:1", Kind: "mobile", Region: "HK"}}}.
		Resolve(ctx, "ns", "pxy_1", "idt_1", "lse_1")
	require.NoError(t, err)
	require.Equal(t, &scheduler.ProxyAssignment{ID: "pxy_1", URL: "http://u:p@h:1", Kind: "mobile", Region: "HK"}, got)

	got, err = schedulerProxyResolver{r: fakeResolver{}}.Resolve(ctx, "ns", "pxy_1", "", "")
	require.NoError(t, err)
	require.Nil(t, got, "a nil assignment stays nil")

	got, err = schedulerProxyResolver{r: fakeResolver{err: apperr.NotFound("proxy not found")}}.Resolve(ctx, "ns", "pxy_1", "", "")
	require.True(t, apperr.IsNotFound(err))
	require.Nil(t, got)
}

type fakeStats struct {
	acquire  stats.AcquireRecord
	leaseEnd stats.LeaseEndRecord
	report   stats.ReportRecord
}

func (f *fakeStats) RecordAcquire(r stats.AcquireRecord)   { f.acquire = r }
func (f *fakeStats) RecordLeaseEnd(r stats.LeaseEndRecord) { f.leaseEnd = r }
func (f *fakeStats) RecordReport(r stats.ReportRecord)     { f.report = r }

func TestStatsAdapters(t *testing.T) {
	rec := &fakeStats{}
	at := time.Unix(1_700_000_000, 0)
	schedulerStats{agg: rec}.RecordAcquire(scheduler.AcquireRecord{At: at, Site: "shop", Result: "ok", Duration: time.Millisecond, Sticky: true})
	require.Equal(t, stats.AcquireRecord{At: at, Site: "shop", Result: "ok", Duration: time.Millisecond, Sticky: true}, rec.acquire)
	schedulerStats{agg: rec}.RecordLeaseEnd(scheduler.LeaseEndRecord{At: at, LeaseID: "lse_1", Kind: "expired", Probe: true})
	require.Equal(t, stats.LeaseEndRecord{At: at, LeaseID: "lse_1", Kind: "expired", Probe: true}, rec.leaseEnd)
	workerStats{agg: rec}.RecordReport(worker.ReportRecord{ReceivedAt: at, ReportID: "r1", HTTPStatus: 429, Markers: []string{"m"}, Late: true})
	require.Equal(t, stats.ReportRecord{ReceivedAt: at, ReportID: "r1", HTTPStatus: 429, Markers: []string{"m"}, Late: true}, rec.report)
}

type fakeExecutor struct {
	in  action.ExecInput
	res action.ExecResult
	err error
}

func (f *fakeExecutor) Execute(_ context.Context, in action.ExecInput) (action.ExecResult, error) {
	f.in = in
	return f.res, f.err
}

func TestWorkerExecutorConvertsInputAndResult(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	site := &catalog.Site{ID: "sit_1"}
	planned := []policy.PlannedAction{{Action: policy.ActionCooldown, Scope: policy.ScopeIdentityEndpoint, Duration: time.Minute}}
	exec := &fakeExecutor{
		res: action.ExecResult{Applied: []action.AppliedAction{{
			Planned: planned[0], SubjectKind: policy.SubjectIdentity, SubjectID: "idt_1", SubjectKey: 7,
			FromState: "active", ToState: "active", Until: now, Skipped: true, SkipReason: "shadow",
		}}},
		err: errors.New("partial"),
	}
	in := worker.ExecInput{
		ReportContext: worker.ReportContext{Site: site, LeaseID: "lse_1", ReportID: "r1", IdentityKey: 7, IdentityID: "idt_1",
			AccountKey: 3, ProxyKey: 4, ProxyID: "pxy_1", Outcome: "rate_limited", RuleName: "rate-limited"},
		Planned: planned, Shadow: true, Now: now,
	}
	res, err := workerExecutor{exec: exec}.Execute(context.Background(), in)
	require.EqualError(t, err, "partial")
	require.Equal(t, action.ExecInput{
		ReportContext: action.ReportContext{Site: site, LeaseID: "lse_1", ReportID: "r1", IdentityKey: 7, IdentityID: "idt_1",
			AccountKey: 3, ProxyKey: 4, ProxyID: "pxy_1", Outcome: "rate_limited", RuleName: "rate-limited"},
		Planned: planned, Shadow: true, Now: now,
	}, exec.in)
	require.Equal(t, worker.ExecResult{Applied: []worker.AppliedAction{{
		Planned: planned[0], SubjectKind: policy.SubjectIdentity, SubjectID: "idt_1",
		FromState: "active", ToState: "active", Until: now, Skipped: true, SkipReason: "shadow",
	}}}, res)
	require.Equal(t, worker.ExecResult{}, convertExecResult(action.ExecResult{}))
}

type fakeSecrets struct {
	v   vault.SecretValue
	err error
}

func (f fakeSecrets) ReadSecret(context.Context, *authz.Principal, *catalog.Namespace, string, int, string) (vault.SecretValue, error) {
	return f.v, f.err
}

func TestConfigSecretsAdapter(t *testing.T) {
	exp := time.Unix(1_800_000_000, 0)
	got, err := configSecrets{store: fakeSecrets{v: vault.SecretValue{Path: "a/b", Version: 3, Value: "v", ExpiresAt: &exp}}}.
		ReadSecret(context.Background(), nil, nil, "a/b", 0, "config:x")
	require.NoError(t, err)
	require.Equal(t, "a/b", got.Path)
	require.Equal(t, 3, got.Version)
	require.Equal(t, "v", got.Value)
	require.Equal(t, &exp, got.ExpiresAt)

	_, err = configSecrets{store: fakeSecrets{err: apperr.PermissionDenied(apperr.ReasonScopeMissing, "no")}}.
		ReadSecret(context.Background(), nil, nil, "a/b", 0, "config:x")
	require.Equal(t, apperr.ReasonScopeMissing, apperr.ReasonOf(err))

	idx, total, err := noMembership{}.Membership(context.Background())
	require.NoError(t, err)
	require.Equal(t, -1, idx)
	require.Zero(t, total)
}

// fakeIdempotentExecutor is an action executor that declares per-report idempotency.
type fakeIdempotentExecutor struct {
	fakeExecutor
	idempotent bool
}

func (f *fakeIdempotentExecutor) IdempotentExecute() bool { return f.idempotent }

func TestWorkerExecutorForwardsIdempotency(t *testing.T) {
	var _ worker.IdempotentActionExecutor = workerExecutor{}
	require.False(t, workerExecutor{exec: &fakeExecutor{}}.IdempotentExecute(),
		"an executor without the declaration is not retried")
	require.False(t, workerExecutor{exec: &fakeIdempotentExecutor{}}.IdempotentExecute())
	require.True(t, workerExecutor{exec: &fakeIdempotentExecutor{idempotent: true}}.IdempotentExecute())
}
