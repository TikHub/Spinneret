package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/proxy/proxydb"
)

// healthCheckAction is the state event action of transitions caused by health checks.
const healthCheckAction = "health_check"

// maxStateReasonLength bounds state_reason written by the checker.
const maxStateReasonLength = 512

// checkTransition is the PostgreSQL effect of one check result.
type checkTransition struct {
	from, to    string
	changed     bool
	attrChanged bool
}

// nextCheckState computes the state, failure streak and next check time after
// a check. Only active → dead (after DeadAfterFailures consecutive failures)
// and dead → active (on success) are automatic transitions.
func nextCheckState(state string, failures int32, ok bool, now time.Time, interval time.Duration) (string, int32, time.Time) {
	if ok {
		failures = 0
	} else {
		failures++
	}
	switch {
	case ok && state == StateDead:
		state = StateActive
	case !ok && state == StateActive && failures >= DeadAfterFailures:
		state = StateDead
	}
	next := now.Add(interval)
	if state == StateDead {
		next = now.Add(deadBackoff(interval, failures))
	}
	return state, failures, next
}

// deadBackoff returns min(interval × 2^(failures − DeadAfterFailures), MaxDeadBackoff).
func deadBackoff(interval time.Duration, failures int32) time.Duration {
	exp := int(failures) - DeadAfterFailures
	if exp < 0 {
		exp = 0
	}
	d := interval
	for i := 0; i < exp && d < MaxDeadBackoff; i++ {
		d *= 2
	}
	return min(d, MaxDeadBackoff)
}

// record stores a check result: check columns and state in PostgreSQL (with a
// state event for transitions), health in Redis for every site of the
// namespace, then hot-state sync and a proxy.state event when the state or
// the location attributes changed.
//
// dueBy is set for scheduled checks (the time the run selected due proxies):
// when the locked row is no longer due, another instance or a manual check
// recorded a check in the meantime and the result is dropped, so failures are
// not counted twice.
func (h *HealthChecker) record(ctx context.Context, proxyID string, res CheckResult, dueBy *time.Time) error {
	now := h.now()
	var (
		row     proxydb.Proxy
		tr      checkTransition
		ns      *catalog.Namespace
		skipped bool
	)
	err := inTx(ctx, h.pool, func(q *proxydb.Queries) error {
		cur, err := q.ProxyGetForUpdate(ctx, proxyID)
		if err != nil {
			return err
		}
		if dueBy != nil && cur.NextCheckAt.After(*dueBy) {
			skipped = true
			return nil
		}
		row = cur
		ns, _ = h.cat.Namespace(cur.NamespaceID)
		arg, t := h.checkUpdate(cur, res, now)
		tr = t
		if err := q.ProxyRecordCheck(ctx, arg); err != nil {
			return fmt.Errorf("record proxy check: %w", err)
		}
		if !tr.changed {
			return nil
		}
		ev := proxydb.ProxyInsertStateEventsParams{
			ID: idgen.New(idgen.StateEvent), CreatedAt: now, NamespaceID: cur.NamespaceID, SubjectKind: auditResourceKind,
			SubjectID: cur.ID, FromState: tr.from, ToState: tr.to, Action: healthCheckAction, Scope: scopeProxy,
			Actor: authz.System("proxy-health").Actor(), Reason: arg.StateReason, Details: json.RawMessage(`{}`),
		}
		if ns != nil {
			ev.TenantID = ns.TenantID
		}
		if _, err := q.ProxyInsertStateEvents(ctx, []proxydb.ProxyInsertStateEventsParams{ev}); err != nil {
			return fmt.Errorf("insert proxy state event: %w", err)
		}
		return nil
	})
	if isNoRows(err) {
		return nil // deleted while being checked
	}
	if err != nil {
		return err
	}
	if skipped || ns == nil {
		return nil
	}
	sctx, cancel := detached(ctx)
	defer cancel()
	if err := recordHealth(sctx, h.rdb, h.keys, sortedSites(ns), row.Hkey, res.OK, now); err != nil {
		h.logger.Warn("proxy health: redis update failed", slog.String("proxy_id", proxyID), slog.Any("error", err))
	}
	if !tr.changed && !tr.attrChanged {
		return nil
	}
	var syncErr error
	if h.hot != nil {
		if err := h.hot.SyncProxies(sctx, ns.ID, []string{proxyID}); err != nil {
			syncErr = fmt.Errorf("sync proxy to hot state: %w", err)
		}
	}
	ev := StateEventData{SubjectID: proxyID, From: tr.from, To: tr.to, Action: healthCheckAction, Reason: res.Error}
	if !tr.changed {
		// Only the location attributes changed: resolvers drop their cached
		// assignment (which carries the region) on this event.
		ev = StateEventData{SubjectID: proxyID, From: tr.from, To: tr.to, Action: "update"}
	}
	if err := publishState(sctx, h.bus, ns, []StateEventData{ev}); err != nil {
		h.logger.Warn("proxy health: publish event failed", slog.String("proxy_id", proxyID), slog.Any("error", err))
	}
	return syncErr
}

// checkUpdate builds the check columns of cur after res.
func (h *HealthChecker) checkUpdate(cur proxydb.Proxy, res CheckResult, now time.Time) (proxydb.ProxyRecordCheckParams, checkTransition) {
	state, failures, next := nextCheckState(cur.State, cur.ConsecutiveCheckFailures, res.OK, now, h.cfg.Interval)
	ok := res.OK
	arg := proxydb.ProxyRecordCheckParams{
		ID: cur.ID, CheckedAt: &now, Ok: &ok, LastLatencyMs: cur.LastLatencyMs, ExitIp: cur.ExitIp,
		ConsecutiveCheckFailures: failures, State: state, StateReason: cur.StateReason,
		StateChangedAt: cur.StateChangedAt, NextCheckAt: next, Region: cur.Region, City: cur.City,
	}
	if res.OK {
		latency := int32(min(res.LatencyMs, int(^uint32(0)>>1)))
		arg.LastLatencyMs = &latency
	}
	if res.ExitIP != "" {
		arg.ExitIp = res.ExitIP
	}
	tr := checkTransition{from: cur.State, to: state, changed: state != cur.State}
	if tr.changed {
		arg.StateChangedAt = now
		if state == StateDead {
			arg.StateReason = truncate(fmt.Sprintf("health check failed %d times: %s", failures, res.Error), maxStateReasonLength)
		} else {
			arg.StateReason = "health check succeeded"
		}
	}
	if cur.Region == "" && res.Region != "" {
		arg.Region = truncate(res.Region, MaxRegionLength)
		tr.attrChanged = true
		if cur.City == "" && res.city != "" {
			arg.City = truncate(res.city, MaxCityLength)
		}
	}
	return arg, tr
}
