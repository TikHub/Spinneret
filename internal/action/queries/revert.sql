-- Rollback of automatic actions (RevertActions, design doc §8.5).

-- name: ActionRevertLifecycleCandidates :many
SELECT DISTINCT ON (e.subject_id)
       e.id, e.created_at, e.site_id, e.subject_id, e.action, e.to_state,
       i.state AS current_state, i.state_changed_at
FROM state_events AS e
JOIN identities AS i ON i.id = e.subject_id
WHERE e.namespace_id = @namespace_id::text
  AND e.subject_kind = 'identity'
  AND e.shadow = false
  AND e.actor = 'system'
  AND e.action = ANY(@actions::text[])
  AND e.site_id = ANY(@site_ids::text[])
  AND e.created_at >= @from_ts::timestamptz
  AND e.created_at < @to_ts::timestamptz
  AND (@policy_id::text = '' OR e.policy_id = @policy_id::text)
  AND (@rule::text = '' OR e.rule = @rule::text)
ORDER BY e.subject_id, e.created_at DESC
LIMIT @max_rows::int;

-- name: ActionRevertAccountCandidates :many
SELECT DISTINCT ON (e.subject_id)
       e.id, e.created_at, e.site_id, e.subject_id
FROM state_events AS e
JOIN accounts AS a ON a.id = e.subject_id
WHERE e.namespace_id = @namespace_id::text
  AND e.subject_kind = 'account'
  AND e.shadow = false
  AND e.actor = 'system'
  AND e.action = 'ban'
  AND e.site_id = ANY(@site_ids::text[])
  AND e.created_at >= @from_ts::timestamptz
  AND e.created_at < @to_ts::timestamptz
  AND (@policy_id::text = '' OR e.policy_id = @policy_id::text)
  AND (@rule::text = '' OR e.rule = @rule::text)
  AND a.state = 'banned'
  -- Still in the state produced by the event: no later lifecycle change of
  -- the account (every account transition records a state event in the same
  -- transaction; attribute edits and cooldowns do not change the state).
  AND NOT EXISTS (
      SELECT 1
      FROM state_events AS l
      WHERE l.subject_id = e.subject_id
        AND l.subject_kind = 'account'
        AND l.shadow = false
        AND l.action <> 'cooldown'
        AND l.created_at > e.created_at
  )
ORDER BY e.subject_id, e.created_at DESC
LIMIT @max_rows::int;

-- name: ActionRevertCooldownCandidates :many
SELECT DISTINCT ON (e.subject_id, e.scope, e.endpoint_group_id)
       e.id, e.created_at, e.site_id, e.subject_id, e.endpoint_group_id, e.scope, e.until,
       i.hkey, i.client
FROM state_events AS e
JOIN identities AS i ON i.id = e.subject_id
WHERE e.namespace_id = @namespace_id::text
  AND e.subject_kind = 'identity'
  AND e.shadow = false
  AND e.actor = 'system'
  AND e.action = 'cooldown'
  AND e.scope IN ('identity_endpoint', 'identity_site')
  AND e.until IS NOT NULL
  AND e.until > @now::timestamptz
  AND e.site_id = ANY(@site_ids::text[])
  AND e.created_at >= @from_ts::timestamptz
  AND e.created_at < @to_ts::timestamptz
  AND (@policy_id::text = '' OR e.policy_id = @policy_id::text)
  AND (@rule::text = '' OR e.rule = @rule::text)
ORDER BY e.subject_id, e.scope, e.endpoint_group_id, e.created_at DESC
LIMIT @max_rows::int;
