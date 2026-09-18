package action

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/action/actiondb"
)

// Batch subject updates. Each statement updates at most one row per subject
// (the batch is de-duplicated first) and never overrides a newer state change.
const (
	updateIdentitiesSQL = `
UPDATE identities AS i
SET state            = u.state,
    state_reason     = u.reason,
    state_changed_at = u.changed_at,
    ban_until        = u.ban_until,
    quarantine_until = u.quarantine_until,
    activated_at     = COALESCE(u.activated_at, i.activated_at),
    updated_at       = u.changed_at
FROM unnest($1::text[], $2::text[], $3::text[], $4::timestamptz[], $5::timestamptz[], $6::timestamptz[], $7::timestamptz[])
     AS u(id, state, reason, changed_at, ban_until, quarantine_until, activated_at)
WHERE i.id = u.id
  AND i.state_changed_at < u.changed_at`

	// Accounts have no state_changed_at column: the time of their last state
	// change is their latest lifecycle state event (every account transition
	// records one in the same transaction), not updated_at, which attribute
	// edits and cooldowns bump as well.
	updateAccountsSQL = `
UPDATE accounts AS a
SET state      = u.state,
    ban_until  = u.ban_until,
    updated_at = GREATEST(a.updated_at, u.changed_at)
FROM unnest($1::text[], $2::text[], $3::timestamptz[], $4::timestamptz[]) AS u(id, state, ban_until, changed_at)
WHERE a.id = u.id
  AND NOT EXISTS (
      SELECT 1
      FROM state_events AS l
      WHERE l.subject_id = a.id
        AND l.subject_kind = 'account'
        AND l.shadow = false
        AND l.action <> 'cooldown'
        AND l.created_at > u.changed_at
  )`

	updateProxiesSQL = `
UPDATE proxies AS p
SET state            = u.state,
    state_reason     = u.reason,
    state_changed_at = u.changed_at,
    ban_until        = u.ban_until,
    updated_at       = u.changed_at
FROM unnest($1::text[], $2::text[], $3::text[], $4::timestamptz[], $5::timestamptz[]) AS u(id, state, reason, changed_at, ban_until)
WHERE p.id = u.id
  AND p.state_changed_at < u.changed_at`
)

// latestUpdates keeps the newest subject update per (kind, id): the latest At
// wins and ties go to the change enqueued last. Subjects keep first-seen order.
func latestUpdates(changes []StateChange) map[string][]StateChange {
	latest := map[[2]string]StateChange{}
	var order [][2]string
	for _, c := range changes {
		if !c.UpdateSubject || c.Shadow || c.SubjectID == "" {
			continue
		}
		k := [2]string{c.SubjectKind, c.SubjectID}
		prev, ok := latest[k]
		if !ok {
			order = append(order, k)
		}
		if !ok || !c.At.Before(prev.At) {
			latest[k] = c
		}
	}
	out := map[string][]StateChange{}
	for _, k := range order {
		out[k[0]] = append(out[k[0]], latest[k])
	}
	return out
}

// updateSubjects applies subject row updates of a batch inside tx.
func updateSubjects(ctx context.Context, tx pgx.Tx, changes []StateChange) error {
	byKind := latestUpdates(changes)
	if rows := byKind[SubjectIdentity]; len(rows) > 0 {
		if err := updateIdentityRows(ctx, tx, rows); err != nil {
			return err
		}
	}
	if rows := byKind[SubjectAccount]; len(rows) > 0 {
		if err := updateAccountRows(ctx, tx, rows); err != nil {
			return err
		}
	}
	if rows := byKind[SubjectProxy]; len(rows) > 0 {
		if err := updateProxyRows(ctx, tx, rows); err != nil {
			return err
		}
	}
	return nil
}

func updateIdentityRows(ctx context.Context, tx pgx.Tx, rows []StateChange) error {
	n := len(rows)
	ids, states, reasons := make([]string, n), make([]string, n), make([]string, n)
	changed := make([]time.Time, n)
	bans, quarantines, activated := make([]*time.Time, n), make([]*time.Time, n), make([]*time.Time, n)
	for i, c := range rows {
		ids[i], states[i], reasons[i], changed[i] = c.SubjectID, c.ToState, stateReason(c), c.At
		switch c.ToState {
		case StateBanned:
			if !c.Permanent {
				bans[i] = c.Until
			}
		case StateQuarantined:
			quarantines[i] = c.Until
		case StateActive:
			at := c.At
			activated[i] = &at
		}
	}
	if _, err := tx.Exec(ctx, updateIdentitiesSQL, ids, states, reasons, changed, bans, quarantines, activated); err != nil {
		return fmt.Errorf("update identity states: %w", err)
	}
	return nil
}

func updateAccountRows(ctx context.Context, tx pgx.Tx, rows []StateChange) error {
	n := len(rows)
	ids, states := make([]string, n), make([]string, n)
	bans, changed := make([]*time.Time, n), make([]time.Time, n)
	for i, c := range rows {
		ids[i], states[i], changed[i] = c.SubjectID, c.ToState, c.At
		if c.ToState == StateBanned && !c.Permanent {
			bans[i] = c.Until
		}
	}
	if _, err := tx.Exec(ctx, updateAccountsSQL, ids, states, bans, changed); err != nil {
		return fmt.Errorf("update account states: %w", err)
	}
	return nil
}

func updateProxyRows(ctx context.Context, tx pgx.Tx, rows []StateChange) error {
	n := len(rows)
	ids, states, reasons := make([]string, n), make([]string, n), make([]string, n)
	changed, until := make([]time.Time, n), make([]*time.Time, n)
	for i, c := range rows {
		ids[i], states[i], reasons[i], changed[i] = c.SubjectID, c.ToState, stateReason(c), c.At
		// proxies have no quarantine_until column: ban_until holds the end of
		// a ban or a quarantine (NULL = permanent ban).
		if !c.Permanent && (c.ToState == StateBanned || c.ToState == StateQuarantined) {
			until[i] = c.Until
		}
	}
	if _, err := tx.Exec(ctx, updateProxiesSQL, ids, states, reasons, changed, until); err != nil {
		return fmt.Errorf("update proxy states: %w", err)
	}
	return nil
}

func stateReason(c StateChange) string {
	switch {
	case c.StateReason != "":
		return c.StateReason
	case c.Rule != "":
		return c.Rule
	default:
		return c.Reason
	}
}

// lookupIDsByKeys maps hot-state hkeys of kind to entity IDs.
func lookupIDsByKeys(ctx context.Context, q actiondb.DBTX, kind string, keys []int64) (map[int64]string, error) {
	db := actiondb.New(q)
	out := make(map[int64]string, len(keys))
	switch kind {
	case SubjectIdentity:
		rows, err := db.ActionIdentityIDsByKeys(ctx, keys)
		if err != nil {
			return nil, fmt.Errorf("resolve identity keys: %w", err)
		}
		for _, r := range rows {
			out[r.Hkey] = r.ID
		}
	case SubjectAccount:
		rows, err := db.ActionAccountIDsByKeys(ctx, keys)
		if err != nil {
			return nil, fmt.Errorf("resolve account keys: %w", err)
		}
		for _, r := range rows {
			out[r.Hkey] = r.ID
		}
	case SubjectProxy:
		rows, err := db.ActionProxyIDsByKeys(ctx, keys)
		if err != nil {
			return nil, fmt.Errorf("resolve proxy keys: %w", err)
		}
		for _, r := range rows {
			out[r.Hkey] = r.ID
		}
	}
	return out, nil
}
