package server

import (
	"context"

	"github.com/Evil0ctal/Spinneret/internal/action"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/configcenter"
	"github.com/Evil0ctal/Spinneret/internal/hotstate"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc"
	"github.com/Evil0ctal/Spinneret/internal/proxy"
	"github.com/Evil0ctal/Spinneret/internal/scheduler"
	"github.com/Evil0ctal/Spinneret/internal/stats"
	"github.com/Evil0ctal/Spinneret/internal/vault"
	"github.com/Evil0ctal/Spinneret/internal/worker"
)

// This file adapts concrete providers to the consumer-declared interfaces of
// other packages whose method signatures use consumer-local mirror types
// (docs/design/6_wiring_interfaces.md). Struct conversions are used where the
// mirror types are identical, so any future divergence fails to compile
// instead of silently dropping fields; the remaining types are copied field by
// field and covered by unit tests.

// hotSyncer is the subset of *hotstate.Syncer the adapters need.
type hotSyncer interface {
	SyncIdentities(ctx context.Context, siteID string, identityIDs []string, opts hotstate.SyncOptions) error
	RemoveIdentities(ctx context.Context, siteID string, identityIDs []string) error
	SyncAccounts(ctx context.Context, siteID string, accountIDs []string) error
	SyncProxies(ctx context.Context, namespaceID string, proxyIDs []string) error
	IdentityHotState(ctx context.Context, site *catalog.Site, identityID string) (hotstate.IdentityHot, error)
}

// identityHotSyncer adapts the hot-state syncer to identitysvc.HotSyncer.
type identityHotSyncer struct{ hot hotSyncer }

var _ identitysvc.HotSyncer = identityHotSyncer{}

func (a identityHotSyncer) SyncIdentities(ctx context.Context, siteID string, identityIDs []string, opts identitysvc.SyncOptions) error {
	return a.hot.SyncIdentities(ctx, siteID, identityIDs, hotstate.SyncOptions(opts))
}

func (a identityHotSyncer) RemoveIdentities(ctx context.Context, siteID string, identityIDs []string) error {
	return a.hot.RemoveIdentities(ctx, siteID, identityIDs)
}

func (a identityHotSyncer) SyncAccounts(ctx context.Context, siteID string, accountIDs []string) error {
	return a.hot.SyncAccounts(ctx, siteID, accountIDs)
}

// identityHotReader adapts the hot-state syncer to identitysvc.HotReader.
type identityHotReader struct{ hot hotSyncer }

var _ identitysvc.HotReader = identityHotReader{}

// IdentityHotState reads the live state and converts it; application errors
// (not found, invalid argument) pass through unchanged.
func (a identityHotReader) IdentityHotState(ctx context.Context, site *catalog.Site, identityID string) (identitysvc.HotState, error) {
	h, err := a.hot.IdentityHotState(ctx, site, identityID)
	if err != nil {
		return identitysvc.HotState{}, err
	}
	return convertIdentityHot(h), nil
}

// convertIdentityHot copies hotstate.IdentityHot into identitysvc.HotState.
// The field order of the two types differs, so a struct conversion is not possible.
func convertIdentityHot(h hotstate.IdentityHot) identitysvc.HotState {
	out := identitysvc.HotState{
		Present:              h.Present,
		State:                h.State,
		ActiveLeases:         h.ActiveLeases,
		SiteCooldownUntil:    h.SiteCooldownUntil,
		SiteReuseUntil:       h.SiteReuseUntil,
		ExclusiveUntil:       h.ExclusiveUntil,
		BoundProxyID:         h.BoundProxyID,
		GlobalScore:          h.GlobalScore,
		GlobalSamples:        h.GlobalSamples,
		AccountCooldownUntil: h.AccountCooldownUntil,
	}
	if h.Groups != nil {
		out.Groups = make([]identitysvc.EndpointHotState, len(h.Groups))
		for i, g := range h.Groups {
			out.Groups[i] = identitysvc.EndpointHotState{
				EndpointGroup:       g.EndpointGroup,
				EndpointGroupID:     g.EndpointGroupID,
				Client:              g.Client,
				Score:               g.Score,
				Samples:             g.Samples,
				ConsecutiveFailures: g.ConsecutiveFailures,
				CooldownUntil:       g.CooldownUntil,
				ReuseUntil:          g.ReuseUntil,
				LastUsedAt:          g.LastUsedAt,
				AvailableAt:         g.AvailableAt,
				InReadyQueue:        g.InReadyQueue,
			}
		}
	}
	return out
}

// actionOperator is the subset of *action.Operator used by identitysvc.
type actionOperator interface {
	OperateIdentities(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, ids []string, req action.OperationRequest) (action.BulkResult, error)
	OperateAccount(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, accountID string, req action.OperationRequest) (action.BulkResult, error)
	RevertActions(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req action.RevertRequest) (action.BulkResult, []string, error)
}

// identityOperator adapts the action operator to identitysvc.Operator.
type identityOperator struct{ op actionOperator }

var _ identitysvc.Operator = identityOperator{}

func (a identityOperator) OperateIdentities(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, ids []string, req identitysvc.OperationRequest) (identitysvc.BulkResult, error) {
	res, err := a.op.OperateIdentities(ctx, p, ns, ids, action.OperationRequest(req))
	return convertBulkResult(res), err
}

func (a identityOperator) OperateAccount(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, accountID string, req identitysvc.OperationRequest) (identitysvc.BulkResult, error) {
	res, err := a.op.OperateAccount(ctx, p, ns, accountID, action.OperationRequest(req))
	return convertBulkResult(res), err
}

func (a identityOperator) RevertActions(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req identitysvc.RevertRequest) (identitysvc.BulkResult, []string, error) {
	res, ids, err := a.op.RevertActions(ctx, p, ns, action.RevertRequest(req))
	return convertBulkResult(res), ids, err
}

// convertBulkResult copies an action.BulkResult; the failure element types
// are distinct named types, so the slice is copied element by element.
func convertBulkResult(r action.BulkResult) identitysvc.BulkResult {
	out := identitysvc.BulkResult{Matched: r.Matched, Succeeded: r.Succeeded}
	if r.Failed != nil {
		out.Failed = make([]identitysvc.BulkFailure, len(r.Failed))
		for i, f := range r.Failed {
			out.Failed[i] = identitysvc.BulkFailure(f)
		}
	}
	return out
}

// actionHotSyncer adapts the hot-state syncer to action.HotSyncer.
type actionHotSyncer struct{ hot hotSyncer }

var _ action.HotSyncer = actionHotSyncer{}

func (a actionHotSyncer) SyncIdentities(ctx context.Context, siteID string, identityIDs []string, opts action.SyncOptions) error {
	return a.hot.SyncIdentities(ctx, siteID, identityIDs, hotstate.SyncOptions(opts))
}

func (a actionHotSyncer) SyncAccounts(ctx context.Context, siteID string, accountIDs []string) error {
	return a.hot.SyncAccounts(ctx, siteID, accountIDs)
}

func (a actionHotSyncer) SyncProxies(ctx context.Context, namespaceID string, proxyIDs []string) error {
	return a.hot.SyncProxies(ctx, namespaceID, proxyIDs)
}

// proxyResolver is the subset of *proxy.Resolver used by the scheduler.
type proxyResolver interface {
	Resolve(ctx context.Context, namespaceID, proxyID, identityID, leaseID string) (*proxy.Assignment, error)
}

// schedulerProxyResolver adapts the proxy resolver to scheduler.ProxyResolver.
type schedulerProxyResolver struct{ r proxyResolver }

var _ scheduler.ProxyResolver = schedulerProxyResolver{}

// Resolve returns nil (and the error) when the resolver fails or returns no
// assignment; the scheduler treats a nil assignment as a render failure.
func (a schedulerProxyResolver) Resolve(ctx context.Context, namespaceID, proxyID, identityID, leaseID string) (*scheduler.ProxyAssignment, error) {
	asg, err := a.r.Resolve(ctx, namespaceID, proxyID, identityID, leaseID)
	if err != nil || asg == nil {
		return nil, err
	}
	out := scheduler.ProxyAssignment(*asg)
	return &out, nil
}

// statsRecorder is the subset of *stats.Aggregator used by the adapters.
type statsRecorder interface {
	RecordAcquire(r stats.AcquireRecord)
	RecordLeaseEnd(r stats.LeaseEndRecord)
	RecordReport(r stats.ReportRecord)
}

// schedulerStats adapts the stats aggregator to scheduler.StatsRecorder.
type schedulerStats struct{ agg statsRecorder }

var _ scheduler.StatsRecorder = schedulerStats{}

func (a schedulerStats) RecordAcquire(r scheduler.AcquireRecord) {
	a.agg.RecordAcquire(stats.AcquireRecord(r))
}

func (a schedulerStats) RecordLeaseEnd(r scheduler.LeaseEndRecord) {
	a.agg.RecordLeaseEnd(stats.LeaseEndRecord(r))
}

// workerStats adapts the stats aggregator to worker.StatsRecorder.
type workerStats struct{ agg statsRecorder }

var _ worker.StatsRecorder = workerStats{}

func (a workerStats) RecordReport(r worker.ReportRecord) { a.agg.RecordReport(stats.ReportRecord(r)) }

// actionExecutor is the subset of *action.Executor used by the worker.
type actionExecutor interface {
	Execute(ctx context.Context, in action.ExecInput) (action.ExecResult, error)
}

// workerExecutor adapts the action executor to worker.ActionExecutor.
type workerExecutor struct{ exec actionExecutor }

var _ worker.IdempotentActionExecutor = workerExecutor{}

// Execute converts the input and the applied actions. action.AppliedAction
// carries an extra SubjectKey that the worker does not use; it is dropped.
func (a workerExecutor) Execute(ctx context.Context, in worker.ExecInput) (worker.ExecResult, error) {
	res, err := a.exec.Execute(ctx, action.ExecInput{
		ReportContext: action.ReportContext(in.ReportContext),
		Planned:       in.Planned,
		Shadow:        in.Shadow,
		Now:           in.Now,
	})
	return convertExecResult(res), err
}

// idempotentExecutor is implemented by an action executor that declares its
// Execute idempotent per report (see worker.IdempotentActionExecutor).
type idempotentExecutor interface {
	IdempotentExecute() bool
}

// IdempotentExecute forwards the idempotency declaration of the action
// executor; the worker retries failed Execute calls only when it is true.
func (a workerExecutor) IdempotentExecute() bool {
	ie, ok := a.exec.(idempotentExecutor)
	return ok && ie.IdempotentExecute()
}

func convertExecResult(r action.ExecResult) worker.ExecResult {
	if r.Applied == nil {
		return worker.ExecResult{}
	}
	out := worker.ExecResult{Applied: make([]worker.AppliedAction, len(r.Applied))}
	for i, a := range r.Applied {
		out.Applied[i] = worker.AppliedAction{
			Planned:     a.Planned,
			SubjectKind: a.SubjectKind,
			SubjectID:   a.SubjectID,
			FromState:   a.FromState,
			ToState:     a.ToState,
			Until:       a.Until,
			Skipped:     a.Skipped,
			SkipReason:  a.SkipReason,
		}
	}
	return out
}

// secretReader is the subset of *vault.SecretStore used by the config center.
type secretReader interface {
	ReadSecret(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, path string, version int, purpose string) (vault.SecretValue, error)
}

// configSecrets adapts the vault secret store to configcenter.SecretReader.
type configSecrets struct{ store secretReader }

var _ configcenter.SecretReader = configSecrets{}

func (a configSecrets) ReadSecret(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, path string, version int, purpose string) (configcenter.SecretValue, error) {
	v, err := a.store.ReadSecret(ctx, p, ns, path, version, purpose)
	if err != nil {
		return configcenter.SecretValue{}, err
	}
	return configcenter.SecretValue(v), nil
}

// noMembership is the proxy.Membership of instances that run no worker: they
// never take part in scheduled proxy health checks.
type noMembership struct{}

var _ proxy.Membership = noMembership{}

func (noMembership) Membership(context.Context) (int, int, error) { return -1, 0, nil }
