package action

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

// StateChange is one lifecycle or disposition change: always a state_events
// row, and for automatic enforced lifecycle changes also an update of the
// subject row (UpdateSubject).
type StateChange struct {
	// ID is assigned by Enqueue when empty.
	ID string
	// At is the time of the change; zero means the enqueue time.
	At          time.Time
	TenantID    string
	NamespaceID string
	SiteID      string
	// SubjectKind is identity, account or proxy.
	SubjectKind string
	// SubjectID identifies the subject; when empty it is resolved from
	// SubjectKey (hkey) at flush time and unresolvable changes are dropped.
	SubjectID       string
	SubjectKey      int64
	EndpointGroupID string
	FromState       string
	ToState         string
	Action          string
	Scope           string
	Until           *time.Time
	Permanent       bool
	Outcome         string
	PolicyID        string
	PolicyVersion   int
	Rule            string
	ReportID        string
	LeaseID         string
	// Actor is "system" or a principal actor ("user:<id>", "token:<id>").
	Actor  string
	Reason string
	Shadow bool
	// Details is stored as the JSON object state_events.details.
	Details map[string]any

	// UpdateSubject also writes ToState (with Until) to the subject row when
	// the change is newer than the row's last state change.
	UpdateSubject bool
	// StateReason is written to state_reason on subject updates; empty means
	// Rule, then Reason.
	StateReason string
}

// stateEventColumns lists the state_events columns written by COPY.
func stateEventColumns() []string {
	return []string{
		"id", "created_at", "tenant_id", "namespace_id", "site_id", "subject_kind", "subject_id",
		"endpoint_group_id", "from_state", "to_state", "action", "scope", "until", "permanent",
		"outcome", "policy_id", "policy_version", "rule", "report_id", "lease_id", "actor", "reason",
		"shadow", "details",
	}
}

// maxDetailsBytes bounds the encoded details of one state event.
const maxDetailsBytes = 8 << 10

// normalize fills the ID and time of a change.
func (c *StateChange) normalize(now time.Time) {
	if c.ID == "" {
		c.ID = idgen.New(idgen.StateEvent)
	}
	if c.At.IsZero() {
		c.At = now
	}
	if c.Actor == "" {
		c.Actor = ActorSystem
	}
}

// insertStateEvents bulk-inserts the state events of changes with COPY.
// Changes without a subject ID are skipped; missing IDs, times and actors are
// filled on copies (the input is not modified).
func insertStateEvents(ctx context.Context, dst postgres.CopyFromer, changes []StateChange) (int64, error) {
	rows := make([]StateChange, 0, len(changes))
	now := time.Now()
	for _, c := range changes {
		if c.SubjectID != "" {
			c.normalize(now)
			rows = append(rows, c)
		}
	}
	n, err := postgres.CopyRows(ctx, dst, "state_events", stateEventColumns(), rows, stateEventValues)
	if err != nil {
		return n, fmt.Errorf("insert state events: %w", err)
	}
	return n, nil
}

// insertStateEventsSQL appends state events and ignores events whose ID was
// already stored (a retried batch whose earlier commit outcome was unknown, or
// a replayed report).
const insertStateEventsSQL = `
INSERT INTO state_events (
    id, created_at, tenant_id, namespace_id, site_id, subject_kind, subject_id,
    endpoint_group_id, from_state, to_state, action, scope, until, permanent,
    outcome, policy_id, policy_version, rule, report_id, lease_id, actor, reason,
    shadow, details)
SELECT u.id, u.created_at, u.tenant_id, u.namespace_id, u.site_id, u.subject_kind, u.subject_id,
       u.endpoint_group_id, u.from_state, u.to_state, u.action, u.scope, u.until, u.permanent,
       u.outcome, u.policy_id, u.policy_version, u.rule, u.report_id, u.lease_id, u.actor, u.reason,
       u.shadow, u.details::jsonb
FROM unnest($1::text[], $2::timestamptz[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[],
            $8::text[], $9::text[], $10::text[], $11::text[], $12::text[], $13::timestamptz[], $14::boolean[],
            $15::text[], $16::text[], $17::integer[], $18::text[], $19::text[], $20::text[], $21::text[], $22::text[],
            $23::boolean[], $24::text[])
     AS u(id, created_at, tenant_id, namespace_id, site_id, subject_kind, subject_id,
          endpoint_group_id, from_state, to_state, action, scope, until, permanent,
          outcome, policy_id, policy_version, rule, report_id, lease_id, actor, reason,
          shadow, details)
ON CONFLICT DO NOTHING`

// stateEventArrays holds the state_events columns of insertStateEventsSQL as
// parallel arrays.
type stateEventArrays struct {
	id, tenant, namespace, site, kind, subject, group, from, to, action, scope []string
	outcome, policy, rule, report, lease, actor, reason, details               []string
	created                                                                    []time.Time
	until                                                                      []*time.Time
	permanent, shadow                                                          []bool
	version                                                                    []int32
}

// add appends the event of c (normalized with now).
func (a *stateEventArrays) add(c StateChange, now time.Time) {
	c.normalize(now)
	a.id = append(a.id, c.ID)
	a.created = append(a.created, c.At)
	a.tenant = append(a.tenant, c.TenantID)
	a.namespace = append(a.namespace, c.NamespaceID)
	a.site = append(a.site, c.SiteID)
	a.kind = append(a.kind, c.SubjectKind)
	a.subject = append(a.subject, c.SubjectID)
	a.group = append(a.group, c.EndpointGroupID)
	a.from = append(a.from, c.FromState)
	a.to = append(a.to, c.ToState)
	a.action = append(a.action, c.Action)
	a.scope = append(a.scope, c.Scope)
	a.until = append(a.until, c.Until)
	a.permanent = append(a.permanent, c.Permanent)
	a.outcome = append(a.outcome, c.Outcome)
	a.policy = append(a.policy, c.PolicyID)
	a.version = append(a.version, int32(c.PolicyVersion))
	a.rule = append(a.rule, c.Rule)
	a.report = append(a.report, c.ReportID)
	a.lease = append(a.lease, c.LeaseID)
	a.actor = append(a.actor, c.Actor)
	a.reason = append(a.reason, c.Reason)
	a.shadow = append(a.shadow, c.Shadow)
	a.details = append(a.details, string(encodeDetails(c.Details)))
}

// args returns the parameters of insertStateEventsSQL.
func (a *stateEventArrays) args() []any {
	return []any{
		a.id, a.created, a.tenant, a.namespace, a.site, a.kind, a.subject,
		a.group, a.from, a.to, a.action, a.scope, a.until, a.permanent,
		a.outcome, a.policy, a.version, a.rule, a.report, a.lease, a.actor, a.reason,
		a.shadow, a.details,
	}
}

// insertStateEventsOnce inserts the state events of changes inside tx,
// skipping events already stored under the same ID. Changes without a
// subject ID are skipped; missing IDs, times and actors are filled on copies.
func insertStateEventsOnce(ctx context.Context, tx pgx.Tx, changes []StateChange) error {
	var cols stateEventArrays
	now := time.Now()
	for _, c := range changes {
		if c.SubjectID != "" {
			cols.add(c, now)
		}
	}
	if len(cols.id) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, insertStateEventsSQL, cols.args()...); err != nil {
		return fmt.Errorf("insert state events: %w", err)
	}
	return nil
}

func stateEventValues(c StateChange) []any {
	return []any{
		c.ID, c.At, c.TenantID, c.NamespaceID, c.SiteID, c.SubjectKind, c.SubjectID,
		c.EndpointGroupID, c.FromState, c.ToState, c.Action, c.Scope, c.Until, c.Permanent,
		c.Outcome, c.PolicyID, int32(c.PolicyVersion), c.Rule, c.ReportID, c.LeaseID, c.Actor, c.Reason,
		c.Shadow, encodeDetails(c.Details),
	}
}

// encodeDetails encodes details as a JSON object, falling back to {} when
// encoding fails or the result is too large.
func encodeDetails(d map[string]any) json.RawMessage {
	if len(d) == 0 {
		return json.RawMessage("{}")
	}
	b, err := json.Marshal(d)
	if err != nil || len(b) > maxDetailsBytes {
		return json.RawMessage(`{"truncated":true}`)
	}
	return b
}

// timePtr returns a pointer to t, or nil for the zero time.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
