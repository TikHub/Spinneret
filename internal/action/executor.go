package action

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

// Executor skip reasons (in addition to the apply.lua reasons).
const (
	SkipShadow          = "shadow"
	SkipNoSubject       = "no_subject"
	SkipInvalidDuration = "invalid_duration"
	SkipUnsupported     = "unsupported"
)

// Modes recorded in the actions_total metric.
const (
	modeEnforce = "enforce"
	modeShadow  = "shadow"
)

// minRecentCooldownRetention is the minimum rcd retention; the effective
// retention is max(2 × breaker window, 10 min).
const minRecentCooldownRetention = 10 * time.Minute

// ExecutorConfig configures the Executor.
type ExecutorConfig struct {
	// RecordCooldownEvents also writes state events for cooldowns
	// (SPINNERET_RECORD_COOLDOWN_EVENTS).
	RecordCooldownEvents bool
}

// Executor applies planned actions of one report to the Redis hot state and
// queues the resulting state changes (spec §6.6). It is safe for concurrent use.
type Executor struct {
	cfg     ExecutorConfig
	apply   *applier
	writer  *StateWriter
	notify  notifier
	metrics *observability.Metrics
	logger  *slog.Logger
	// enqueueWait bounds the wait for state writer queue space of one
	// report's lifecycle changes (maxEnqueueWait).
	enqueueWait time.Duration
}

// NewExecutor creates an executor. bus, metrics and logger may be nil.
func NewExecutor(cfg ExecutorConfig, rdb rueidis.Client, keys redis.Keys, writer *StateWriter, bus events.Bus, metrics *observability.Metrics, logger *slog.Logger) *Executor {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	logger = logger.With("component", "action.executor")
	return &Executor{
		cfg:         cfg,
		apply:       newApplier(rdb, keys),
		writer:      writer,
		notify:      notifier{bus: bus, logger: logger},
		metrics:     metrics,
		logger:      logger,
		enqueueWait: maxEnqueueWait,
	}
}

// plan is one planned action translated into an apply.lua operation.
type plan struct {
	result AppliedAction
	op     luaOp
	scope  policy.ActionScope // effective scope (after account degradation)
	global bool               // proxy-global scope: applied on every site of the namespace
	valid  bool
	lua    luaResult
}

// Execute applies in.Planned. In shadow mode (in.Shadow, or an action policy
// of the report's endpoint group in mode shadow) nothing is applied and state
// events are recorded with shadow=true. Errors are returned only when the hot
// state of the report's site could not be updated.
//
// Execute is idempotent per report: the apply.lua calls carry the lease and
// report ID as idempotency token, so a retry with the same input (for
// example after a client-side timeout of a call that did run in Redis) gets
// the results of the first run and queues the same state changes and events
// instead of reporting already_banned. The state changes of a report are
// therefore persisted even when an earlier attempt failed after applying; a
// retry that follows a successful attempt enqueues them again, and the
// StateWriter keeps state_events free of duplicates by their deterministic ID.
func (e *Executor) Execute(ctx context.Context, in ExecInput) (ExecResult, error) {
	if in.Namespace == nil || in.Site == nil {
		return ExecResult{}, errors.New("execute actions: namespace and site are required")
	}
	if len(in.Planned) == 0 {
		return ExecResult{}, nil
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	plans := make([]plan, len(in.Planned))
	for i, pa := range in.Planned {
		plans[i] = e.buildPlan(&in, pa, now)
	}
	if in.Shadow || (in.Group != nil && in.Group.Action.Shadow()) {
		return e.finishShadow(ctx, &in, plans, now), nil
	}
	if err := e.applyPlans(ctx, &in, plans, now); err != nil {
		return ExecResult{}, err
	}
	return e.finishEnforced(ctx, &in, plans, now), nil
}

// buildPlan maps a planned action to its subject and apply.lua operation.
func (e *Executor) buildPlan(in *ExecInput, pa policy.PlannedAction, now time.Time) plan {
	p := plan{result: AppliedAction{Planned: pa, SubjectKind: pa.Scope.Subject()}, scope: pa.Scope}
	if pa.Scope == policy.ScopeAccount && in.AccountKey == 0 {
		// Account-scoped rules degrade to the identity when it has no account
		// (policy already does this; kept as a safety net).
		p.scope = policy.ScopeIdentity
		if pa.Action == policy.ActionCooldown {
			p.scope = policy.ScopeIdentitySite
		}
	}
	if pa.Action == policy.ActionCooldown && p.scope == policy.ScopeIdentity {
		p.scope = policy.ScopeIdentitySite
	}
	subject := p.scope.Subject()
	p.result.SubjectKind = subject
	switch subject {
	case policy.SubjectIdentity:
		p.result.SubjectID, p.result.SubjectKey = in.IdentityID, in.IdentityKey
	case policy.SubjectAccount:
		p.result.SubjectKey = in.AccountKey
	case policy.SubjectProxy:
		p.result.SubjectID, p.result.SubjectKey = in.ProxyID, in.ProxyKey
	}
	if p.result.SubjectKey <= 0 {
		return p.skip(SkipNoSubject)
	}
	var until time.Time
	switch pa.Action {
	case policy.ActionCooldown, policy.ActionQuarantine:
		if pa.Duration <= 0 {
			return p.skip(SkipInvalidDuration)
		}
		until = now.Add(pa.Duration)
	case policy.ActionBan:
		if !pa.Permanent {
			if pa.Duration <= 0 {
				return p.skip(SkipInvalidDuration)
			}
			until = now.Add(pa.Duration)
		}
	}
	p.result.Until = until
	op, global, ok := e.buildOp(in, pa, p.scope, untilMs(until, pa.Action == policy.ActionBan && pa.Permanent))
	if !ok {
		return p.skip(SkipUnsupported)
	}
	p.op, p.global, p.valid = op, global, true
	return p
}

func (p plan) skip(reason string) plan {
	p.result.Skipped, p.result.SkipReason, p.valid = true, reason, false
	return p
}

// buildOp returns the apply.lua operation of an action at scope.
func (e *Executor) buildOp(in *ExecInput, pa policy.PlannedAction, scope policy.ActionScope, until int64) (luaOp, bool, bool) {
	client := ""
	if in.Group != nil {
		client = in.Group.Client
	}
	// Endpoint groups of the identity's client, computed only for the
	// identity-level operations that push or remove the identity.
	identityGroups := func() []int64 { return groupKeys(in.Site, client) }
	switch pa.Action {
	case policy.ActionCooldown:
		switch scope {
		case policy.ScopeIdentityEndpoint:
			if in.Group == nil {
				return luaOp{}, false, false
			}
			op := luaOp{Op: luaCooldown, Scope: luaScopeIdentityEndpoint, Subject: in.IdentityKey, Group: in.Group.Key,
				Until: until, Flags: flagAutomatic, Trim: recentCooldownRetention(in).Milliseconds(), PrevFail: streakIncrement(in.Outcome)}
			if in.Group.Action != nil {
				op.Baseline = in.Group.Action.Health.Baseline
			}
			return op, false, true
		case policy.ScopeIdentitySite:
			return luaOp{Op: luaCooldown, Scope: luaScopeIdentitySite, Subject: in.IdentityKey, Until: until,
				Flags: flagAutomatic, Groups: identityGroups()}, false, true
		case policy.ScopeAccount:
			return luaOp{Op: luaCooldown, Scope: luaScopeAccount, Subject: in.AccountKey, Until: until,
				Flags: flagAutomatic, Groups: groupKeys(in.Site, "")}, false, true
		case policy.ScopeProxySite:
			return luaOp{Op: luaCooldown, Scope: luaScopeProxySite, Subject: in.ProxyKey, Until: until, Flags: flagAutomatic}, false, true
		case policy.ScopeProxy:
			return luaOp{Op: luaCooldown, Scope: luaScopeProxyGlobal, Subject: in.ProxyKey, Until: until, Flags: flagAutomatic}, true, true
		}
	case policy.ActionBan:
		switch scope {
		case policy.ScopeIdentity:
			return luaOp{Op: luaBan, Scope: luaScopeIdentity, Subject: in.IdentityKey, Until: until,
				Flags: flagAutomatic | flagRecordBans, Groups: identityGroups()}, false, true
		case policy.ScopeAccount:
			return luaOp{Op: luaBan, Scope: luaScopeAccount, Subject: in.AccountKey, Until: until,
				Flags: flagAutomatic | flagRecordBans, Groups: groupKeys(in.Site, "")}, false, true
		case policy.ScopeProxy:
			return luaOp{Op: luaBan, Scope: luaScopeProxy, Subject: in.ProxyKey, Until: until, Flags: flagAutomatic}, true, true
		}
	case policy.ActionExpire:
		if scope == policy.ScopeIdentity {
			return luaOp{Op: luaExpire, Scope: luaScopeIdentity, Subject: in.IdentityKey, Groups: identityGroups()}, false, true
		}
	case policy.ActionQuarantine:
		switch scope {
		case policy.ScopeIdentity:
			return luaOp{Op: luaQuarantine, Scope: luaScopeIdentity, Subject: in.IdentityKey, Until: until, Groups: identityGroups()}, false, true
		case policy.ScopeProxy:
			return luaOp{Op: luaQuarantine, Scope: luaScopeProxy, Subject: in.ProxyKey, Until: until}, true, true
		}
	case policy.ActionActivate:
		if scope == policy.ScopeIdentity {
			return luaOp{Op: luaActivate, Scope: luaScopeIdentity, Subject: in.IdentityKey, Groups: identityGroups()}, false, true
		}
	}
	return luaOp{}, false, false
}

// applyPlans runs apply.lua on the report's site and, for proxy-global scopes,
// on every other site of the namespace.
func (e *Executor) applyPlans(ctx context.Context, in *ExecInput, plans []plan, now time.Time) error {
	var mainOps, globalOps []luaOp
	var mainIdx, globalIdx []int
	for i := range plans {
		if !plans[i].valid {
			continue
		}
		mainOps, mainIdx = append(mainOps, plans[i].op), append(mainIdx, i)
		if plans[i].global {
			globalOps, globalIdx = append(globalOps, plans[i].op), append(globalIdx, i)
		}
	}
	if len(mainOps) == 0 {
		return nil
	}
	token := replayToken(in)
	calls := []siteCall{{SiteKey: in.Site.Key, Ops: mainOps, Token: token}}
	if len(globalOps) > 0 {
		for _, s := range in.Namespace.SitesByID {
			if s.ID != in.Site.ID {
				calls = append(calls, siteCall{SiteKey: s.Key, Ops: globalOps, Token: token})
			}
		}
	}
	results, errs := e.apply.runMulti(ctx, now, calls)
	if errs[0] != nil {
		return fmt.Errorf("apply actions on site %s: %w", in.Site.ID, errs[0])
	}
	for j, idx := range mainIdx {
		plans[idx].lua = results[0][j]
	}
	for c := 1; c < len(calls); c++ {
		if errs[c] != nil {
			e.logger.Warn("apply proxy action on peer site failed", "site_key", calls[c].SiteKey, "error", errs[c])
			continue
		}
		for j, idx := range globalIdx {
			r := results[c][j]
			if r.Applied && !plans[idx].lua.Applied && plans[idx].lua.Reason == skipMissing {
				// The proxy is not materialized on the report's site but changed
				// elsewhere: report that change. A skip on the report's site
				// (e.g. already banned) stands, so that a lagging peer site
				// never downgrades the recorded state.
				plans[idx].lua = r
			}
		}
	}
	for i := range plans {
		if !plans[i].valid {
			continue
		}
		r := plans[i].lua
		res := &plans[i].result
		res.FromState, res.ToState = r.From, r.To
		res.Skipped, res.SkipReason = !r.Applied, r.Reason
		if r.Until > 0 {
			res.Until = msTime(r.Until)
		} else {
			res.Until = time.Time{}
		}
	}
	return nil
}

// replayToken is the prefix of the apply.lua idempotency tokens of a
// report's actions: the lease and report IDs (empty when the report has no
// ID). The applier completes it with a digest of each call's arguments, so a
// retry of the same call replays its recorded results while a different set
// of operations for the same report is applied normally.
func replayToken(in *ExecInput) string {
	if in.ReportID == "" {
		return ""
	}
	return "r:" + in.LeaseID + ":" + in.ReportID
}

// finishEnforced records metrics, state changes and bus events of applied actions.
func (e *Executor) finishEnforced(ctx context.Context, in *ExecInput, plans []plan, now time.Time) ExecResult {
	out := ExecResult{Applied: make([]AppliedAction, len(plans))}
	var changes []StateChange
	for i := range plans {
		p := &plans[i]
		out.Applied[i] = p.result
		if !p.valid || p.result.Skipped {
			continue
		}
		e.countAction(in, p, modeEnforce)
		cooldown := p.result.Planned.Action == policy.ActionCooldown
		if cooldown && !e.cfg.RecordCooldownEvents {
			continue
		}
		c := e.baseChange(in, p, now)
		c.UpdateSubject = !cooldown
		c.ID = reportEventID(in, c, i, -1)
		changes = append(changes, c)
		for j, m := range p.lua.Members {
			mc := c
			mc.SubjectKind, mc.SubjectID, mc.SubjectKey = SubjectIdentity, m.ID, m.Key
			mc.FromState, mc.ToState = m.From, m.To
			mc.StateReason = ReasonAccountBan
			mc.ID = reportEventID(in, mc, i, j)
			changes = append(changes, mc)
		}
	}
	e.enqueue(ctx, changes)
	e.notify.publishAll(ctx, changes)
	return out
}

// finishShadow records shadow state events for every valid planned action.
func (e *Executor) finishShadow(ctx context.Context, in *ExecInput, plans []plan, now time.Time) ExecResult {
	out := ExecResult{Applied: make([]AppliedAction, len(plans))}
	var changes []StateChange
	for i := range plans {
		p := &plans[i]
		if p.valid {
			p.result.Skipped, p.result.SkipReason = true, SkipShadow
			p.result.ToState = targetState(p.result.Planned.Action)
			e.countAction(in, p, modeShadow)
			if p.result.Planned.Action != policy.ActionCooldown || e.cfg.RecordCooldownEvents {
				c := e.baseChange(in, p, now)
				c.Shadow = true
				c.ID = reportEventID(in, c, i, -1)
				changes = append(changes, c)
			}
		}
		out.Applied[i] = p.result
	}
	e.enqueue(ctx, changes)
	return out
}

// baseChange builds the state change of an applied (or shadowed) plan.
func (e *Executor) baseChange(in *ExecInput, p *plan, now time.Time) StateChange {
	pa := p.result.Planned
	c := StateChange{
		At:          now,
		TenantID:    in.Namespace.TenantID,
		NamespaceID: in.Namespace.ID,
		SiteID:      in.Site.ID,
		SubjectKind: string(p.result.SubjectKind),
		SubjectID:   p.result.SubjectID,
		SubjectKey:  p.result.SubjectKey,
		FromState:   p.result.FromState,
		ToState:     p.result.ToState,
		Action:      string(pa.Action),
		Scope:       string(p.scope),
		Until:       timePtr(p.result.Until),
		Permanent:   pa.Action == policy.ActionBan && pa.Permanent,
		Outcome:     in.Outcome,
		Rule:        pa.RuleName,
		ReportID:    in.ReportID,
		LeaseID:     in.LeaseID,
		Actor:       ActorSystem,
		Reason:      pa.RuleName,
		Details:     map[string]any{"source": pa.Source, "severity": policy.Severity(pa)},
	}
	if pa.Duration > 0 {
		c.Details["duration_ms"] = pa.Duration.Milliseconds()
	}
	if in.RuleName != "" {
		c.Details["signal_rule"] = in.RuleName
	}
	if in.Group != nil {
		c.EndpointGroupID = in.Group.ID
		c.PolicyID = in.Group.ActionRef.PolicyID
		c.PolicyVersion = in.Group.ActionRef.Version
	}
	return c
}

// enqueue queues the state changes of a report. Lifecycle changes wait for
// queue space (backpressure on the worker), also when ctx has ended because
// the changes are already applied in Redis. The wait is bounded once per
// report by enqueueWait: after it every further lifecycle change that finds
// the queue full is dropped (and counted by the state writer) right away, so
// a report with many changes (an account ban and its members) never blocks
// the worker for longer.
func (e *Executor) enqueue(ctx context.Context, changes []StateChange) {
	if e.writer == nil || len(changes) == 0 {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.enqueueWait)
	defer cancel()
	var dropped int
	var firstErr error
	for _, c := range changes {
		if err := e.writer.EnqueueContext(wctx, c); err != nil {
			if dropped == 0 {
				firstErr = err
			}
			dropped++
		}
	}
	if dropped > 0 {
		e.logger.Error("state changes of a report not queued", "report_id", changes[0].ReportID,
			"lease_id", changes[0].LeaseID, "dropped", dropped, "changes", len(changes), "error", firstErr)
	}
}

// IdempotentExecute reports that Execute is idempotent per report (see
// Execute), so the worker may retry a failed call with the same input
// (worker.IdempotentActionExecutor). Reports always carry a report ID, which
// the idempotency token requires.
func (e *Executor) IdempotentExecute() bool { return true }

// reportEventID derives the ID of the state event c of a report from the
// lease and report IDs and the change itself (subject, action, scope, time
// and end), so a retried report produces the same IDs and its events are
// stored once. plan is the index of the planned action, member the index of
// the member identity of an account action (-1 for the action's own event).
// Like the random IDs it starts with the change time in milliseconds (48
// bits), followed by the plan and member positions (16 bits each, so the
// events of one report sort in plan order) and 48 bits of a SHA-256 digest.
// It returns "" (a random ID is assigned on enqueue) when the report has no
// ID.
func reportEventID(in *ExecInput, c StateChange, plan, member int) string {
	if in.ReportID == "" {
		return ""
	}
	h := sha256.New()
	until := int64(0)
	if c.Until != nil {
		until = c.Until.UnixNano()
	}
	for _, part := range []string{
		in.LeaseID, in.ReportID, c.SubjectKind, c.SubjectID, strconv.FormatInt(c.SubjectKey, 10), c.Action, c.Scope,
		strconv.FormatBool(c.Shadow), strconv.FormatInt(c.At.UnixNano(), 10), strconv.FormatInt(until, 10),
		strconv.Itoa(plan), strconv.Itoa(member),
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	var raw [16]byte
	ms := uint64(max(c.At.UnixMilli(), 0)) & (1<<48 - 1)
	binary.BigEndian.PutUint16(raw[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(raw[2:6], uint32(ms))
	binary.BigEndian.PutUint16(raw[6:8], uint16(min(max(plan, 0), math.MaxUint16)))
	binary.BigEndian.PutUint16(raw[8:10], uint16(min(max(member+1, 0), math.MaxUint16)))
	copy(raw[10:], sum)
	return idgen.StateEvent + "_" + hex.EncodeToString(raw[:])
}

func (e *Executor) countAction(in *ExecInput, p *plan, mode string) {
	if e.metrics == nil {
		return
	}
	e.metrics.ActionsTotal.WithLabelValues(
		observability.Label(in.Site.Name), string(p.result.Planned.Action), string(p.scope), mode,
	).Inc()
}

// targetState is the lifecycle state an action leads to ("" for cooldowns).
func targetState(a policy.ActionKind) string {
	switch a {
	case policy.ActionBan:
		return StateBanned
	case policy.ActionExpire:
		return StateExpired
	case policy.ActionQuarantine:
		return StateQuarantined
	case policy.ActionActivate:
		return StateActive
	}
	return ""
}

// streakIncrement is 1 when observe.lua already counted the report in the
// identity × endpoint failure streak.
func streakIncrement(outcome string) int {
	if policy.IsFailureOutcome(outcome) || outcome == policy.OutcomeNetworkError {
		return 1
	}
	return 0
}

// recentCooldownRetention is how long rcd entries are kept for breaker reverts.
func recentCooldownRetention(in *ExecInput) time.Duration {
	d := minRecentCooldownRetention
	if in.Group != nil && in.Group.Breaker != nil {
		d = max(d, 2*in.Group.Breaker.Window.Std())
	}
	return d
}
