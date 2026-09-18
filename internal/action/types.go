// Package action executes dispositions decided by action policies against the
// Redis hot state (apply.lua), persists lifecycle changes and state events to
// PostgreSQL (StateWriter), and implements manual identity/account operations,
// rollback of automatic actions and ban/quarantine expiry (spec §6.6, design
// doc §8 and §9.3).
//
// Write ordering: automatic actions are Redis-first (the worker hot path) and
// reach PostgreSQL asynchronously through the StateWriter; manual operations,
// reverts and expiry are PostgreSQL-first and then pushed to Redis (apply.lua
// for the immediate effect, HotSyncer for the authoritative materialization).
package action

import (
	"context"
	"time"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/pkg/durationx"
	"github.com/TikHub/Spinneret/internal/policy"
)

// Identity lifecycle states (spec §4 identities.state).
const (
	StatePending     = "pending"
	StateActive      = "active"
	StateExpired     = "expired"
	StateBanned      = "banned"
	StateQuarantined = "quarantined"
	StateDisabled    = "disabled"
	StateRetired     = "retired"
)

// Manual identity operations (IdentityAdminService.OperateIdentities) and the
// action names recorded in state events.
const (
	OpCooldown     = "cooldown"
	OpBan          = "ban"
	OpUnban        = "unban"
	OpQuarantine   = "quarantine"
	OpUnquarantine = "unquarantine"
	OpExpire       = "expire"
	OpDisable      = "disable"
	OpEnable       = "enable"
	OpArchive      = "archive"
	OpRestore      = "restore"
	OpActivate     = "activate"
	OpResetStats   = "reset_stats"
	OpRevert       = "revert"
)

// Subject kinds of state events.
const (
	SubjectIdentity = string(policy.SubjectIdentity)
	SubjectAccount  = string(policy.SubjectAccount)
	SubjectProxy    = string(policy.SubjectProxy)
)

// ActorSystem is the state event actor of automatic changes.
const ActorSystem = "system"

// State reasons stored on member identities whose state was changed because of
// their account; account unban/enable only releases members carrying them.
const (
	ReasonAccountBan      = "account_ban"
	ReasonAccountDisabled = "account_disabled"
	ReasonBanExpired      = "ban_expired"
	ReasonQuarantineEnded = "quarantine_expired"
	ReasonReverted        = "reverted"
)

// Bulk failure reasons.
const (
	FailureNotFound          = "not_found"
	FailurePermissionDenied  = "permission_denied"
	FailureInvalidTransition = "invalid_transition"
	FailureInvalidArgument   = "invalid_argument"
	FailureNotInHotState     = "not_in_hot_state"
	FailureStateChanged      = "state_changed"
	FailureInternal          = "internal"
)

// Limits.
const (
	// MaxOperationIDs is the maximum number of identity IDs per OperateIdentities call.
	MaxOperationIDs = 10000
	// MaxRevertIDs is the maximum number of affected identity IDs returned by RevertActions.
	MaxRevertIDs = 1000
	// maxRevertCandidates bounds the state events considered by one revert.
	maxRevertCandidates = 50000
	// dbChunk is the number of rows changed per PostgreSQL transaction.
	dbChunk = 500
)

// ReportContext identifies the report whose planned actions are executed.
// Namespace, Site and Group come from the catalog snapshot of the lease; keys
// are hot-state hkeys (0 when absent).
type ReportContext struct {
	Namespace   *catalog.Namespace
	Site        *catalog.Site
	Group       *catalog.EndpointGroup
	LeaseID     string
	ReportID    string
	IdentityKey int64
	IdentityID  string
	AccountKey  int64
	ProxyKey    int64
	ProxyID     string
	Outcome     string
	// RuleName is the signal rule that classified the report (recorded in
	// state event details).
	RuleName string
}

// ExecInput is one report's planned actions.
type ExecInput struct {
	ReportContext
	Planned []policy.PlannedAction
	// Shadow records state events with shadow=true without touching Redis.
	Shadow bool
	// Now is the evaluation time; zero means time.Now().
	Now time.Time
}

// AppliedAction is the outcome of one planned action.
type AppliedAction struct {
	Planned     policy.PlannedAction
	SubjectKind policy.SubjectKind
	// SubjectID is the identity or proxy ID; it is empty for accounts because
	// the report context only carries the account hkey (see SubjectKey).
	SubjectID string
	// SubjectKey is the hot-state hkey of the subject (addition to the
	// documented contract).
	SubjectKey int64
	FromState  string
	ToState    string
	// Until is the effective end of the cooldown/ban/quarantine; zero for
	// permanent bans, expiry and activation.
	Until      time.Time
	Skipped    bool
	SkipReason string
}

// ExecResult lists the outcome of every planned action in input order.
type ExecResult struct {
	Applied []AppliedAction
}

// OperationRequest is a manual operation on identities or an account.
type OperationRequest struct {
	// Operation is one of the Op* constants.
	Operation string
	// Scope of cooldown and reset_stats: identity_endpoint, identity_site,
	// identity (≡ identity_site) or "" (cooldown: identity_site; reset_stats:
	// every level).
	Scope string
	// EndpointGroupID is required for scope identity_endpoint.
	EndpointGroupID string
	// Duration of cooldown, ban (durationx.Permanent allowed) and quarantine
	// (zero: action policy default).
	Duration durationx.Duration
	Reason   string
	// ResetFailures and ResetHealth apply to unban, unquarantine, enable,
	// restore and activate.
	ResetFailures, ResetHealth bool
}

// RevertRequest selects automatic actions to roll back.
type RevertRequest struct {
	// SiteID restricts the revert to one site ("" = every site the principal may operate).
	SiteID   string
	PolicyID string
	Rule     string
	// Actions to revert: ban, quarantine, expire, cooldown (empty = all four).
	Actions []string
	// From is required; a zero To means now.
	From, To                   time.Time
	ResetFailures, ResetHealth bool
	DryRun                     bool
}

// BulkResult summarizes a bulk operation.
type BulkResult struct {
	Matched, Succeeded int
	Failed             []BulkFailure
}

// BulkFailure describes one item that could not be processed.
type BulkFailure struct {
	ID, Reason, Message string
}

// SyncOptions mirrors hotstate.SyncOptions; the server adapts the hotstate
// syncer to HotSyncer.
type SyncOptions struct {
	ResetHealth, ResetFailures bool
}

// HotSyncer materializes PostgreSQL state into the Redis hot state (provided
// by hotstate.Syncer through an adapter).
type HotSyncer interface {
	SyncIdentities(ctx context.Context, siteID string, identityIDs []string, opts SyncOptions) error
	SyncAccounts(ctx context.Context, siteID string, accountIDs []string) error
	SyncProxies(ctx context.Context, namespaceID string, proxyIDs []string) error
}

// schedulable reports whether identities in state take part in scheduling.
func schedulable(state string) bool {
	return state == StateActive || state == StatePending
}
