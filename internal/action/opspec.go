package action

import (
	"time"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/site"
)

// opKind classifies manual identity operations.
type opKind int

const (
	kindLifecycle  opKind = iota // PostgreSQL state transition
	kindCooldown                 // Redis-only cooldown
	kindResetStats               // Redis-only statistics reset
)

// Cooldown / reset_stats levels.
const (
	scopeAll      = ""
	scopeEndpoint = string(policy.ScopeIdentityEndpoint)
	scopeSite     = string(policy.ScopeIdentitySite)
)

// opSpec is a validated OperationRequest.
type opSpec struct {
	op         string
	kind       opKind
	to         string
	from       []string
	scope      string
	groupID    string
	duration   time.Duration
	permanent  bool
	resetFlags bool
	resetFail  bool
	resetHP    bool
}

// transition returns the target state and allowed source states of a
// lifecycle operation (design doc §9.3).
func transition(op string) (to string, from []string, ok bool) {
	switch op {
	case OpBan:
		return StateBanned, []string{StateActive, StatePending, StateQuarantined, StateExpired, StateDisabled}, true
	case OpUnban:
		return StatePending, []string{StateBanned}, true
	case OpQuarantine:
		return StateQuarantined, []string{StateActive, StatePending}, true
	case OpUnquarantine:
		return StatePending, []string{StateQuarantined}, true
	case OpExpire:
		return StateExpired, []string{StateActive, StatePending, StateQuarantined}, true
	case OpDisable:
		return StateDisabled, []string{StatePending, StateActive, StateExpired, StateBanned, StateQuarantined}, true
	case OpEnable:
		return StateActive, []string{StateDisabled}, true
	case OpArchive:
		return StateRetired, []string{StatePending, StateActive, StateExpired, StateBanned, StateQuarantined, StateDisabled}, true
	case OpRestore:
		return StatePending, []string{StateRetired}, true
	case OpActivate:
		return StateActive, []string{StatePending}, true
	}
	return "", nil, false
}

// resetApplies reports whether reset_failures/reset_health apply to op.
func resetApplies(op string) bool {
	switch op {
	case OpUnban, OpUnquarantine, OpEnable, OpRestore, OpActivate:
		return true
	}
	return false
}

// newOpSpec validates req.
func newOpSpec(req OperationRequest) (opSpec, error) {
	s := opSpec{op: req.Operation, groupID: req.EndpointGroupID}
	if to, from, ok := transition(req.Operation); ok {
		s.kind, s.to, s.from = kindLifecycle, to, from
		s.resetFlags = resetApplies(req.Operation)
		s.resetFail, s.resetHP = s.resetFlags && req.ResetFailures, s.resetFlags && req.ResetHealth
	} else {
		switch req.Operation {
		case OpCooldown:
			s.kind = kindCooldown
		case OpResetStats:
			s.kind = kindResetStats
		default:
			return s, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "unknown operation %q", req.Operation)
		}
	}
	if err := s.validateScope(req); err != nil {
		return s, err
	}
	return s, s.validateDuration(req)
}

func (s *opSpec) validateScope(req OperationRequest) error {
	if s.kind == kindLifecycle {
		return nil
	}
	switch req.Scope {
	case scopeAll:
		s.scope = scopeAll
		if s.kind == kindCooldown {
			s.scope = scopeSite
		}
	case scopeSite, string(policy.ScopeIdentity):
		s.scope = scopeSite
	case scopeEndpoint:
		s.scope = scopeEndpoint
		if req.EndpointGroupID == "" {
			return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "endpoint_group_id is required for scope identity_endpoint")
		}
	default:
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "invalid scope %q", req.Scope)
	}
	return nil
}

func (s *opSpec) validateDuration(req OperationRequest) error {
	d := req.Duration
	switch s.op {
	case OpBan:
		if d.IsPermanent() {
			s.permanent = true
			return nil
		}
		if d <= 0 {
			return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "duration is required for ban")
		}
	case OpCooldown:
		if d.IsPermanent() || d <= 0 {
			return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "cooldown requires a positive duration")
		}
	case OpQuarantine:
		if d.IsPermanent() || d < 0 {
			return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "quarantine duration must be a positive duration")
		}
	default:
		return nil
	}
	s.duration = d.Std()
	return nil
}

// check validates one identity against the operation; it returns a failure
// reason and message, or "" when the identity may be processed.
func (s opSpec) check(st *catalog.Site, row identityRow) (string, string) {
	switch s.kind {
	case kindLifecycle:
		if !contains(s.from, row.State) {
			return FailureInvalidTransition, "cannot " + s.op + " an identity in state " + row.State
		}
	case kindCooldown, kindResetStats:
		if s.kind == kindCooldown && row.State == StateRetired {
			return FailureInvalidTransition, "cannot cool down a retired identity"
		}
		if s.scope == scopeEndpoint {
			g, ok := st.GroupsByID[s.groupID]
			if !ok || g.Client != row.Client {
				return FailureInvalidArgument, "endpoint group does not belong to the identity's site and client"
			}
		}
	}
	return "", ""
}

// quarantineDuration returns the explicit duration or the action policy
// default of the site+client "_default" group (24h when unavailable).
func (s opSpec) quarantineDuration(st *catalog.Site, client string) time.Duration {
	if s.duration > 0 {
		return s.duration
	}
	if g, ok := st.Group(client, site.DefaultGroup); ok && g.Action != nil {
		if d := g.Action.Health.QuarantineDuration.Std(); d > 0 {
			return d
		}
	}
	return policy.DefaultQuarantineDuration
}

// until returns the end of a lifecycle transition (nil when none or permanent).
func (s opSpec) until(st *catalog.Site, client string, now time.Time) *time.Time {
	var d time.Duration
	switch s.to {
	case StateBanned:
		if s.permanent {
			return nil
		}
		d = s.duration
	case StateQuarantined:
		d = s.quarantineDuration(st, client)
	default:
		return nil
	}
	t := now.Add(d)
	return &t
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
