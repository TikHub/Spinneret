-- Ban / quarantine expiry (leader job, spec §6.8).

-- name: ActionDueIdentityBans :many
SELECT i.id, i.site_id, i.client
FROM identities AS i
WHERE i.state = 'banned'
  AND i.ban_until IS NOT NULL
  AND i.ban_until <= @now::timestamptz
  AND NOT EXISTS (
      SELECT 1 FROM accounts AS a
      WHERE a.id = i.account_id
        AND a.state = 'banned'
        AND (a.ban_until IS NULL OR a.ban_until > @now::timestamptz)
  )
ORDER BY i.ban_until
LIMIT @max_rows::int;

-- name: ActionReleaseDueBans :many
UPDATE identities AS i
SET state            = @to_state::text,
    state_reason     = @reason::text,
    state_changed_at = @now::timestamptz,
    ban_until        = NULL,
    quarantine_until = NULL,
    activated_at     = CASE WHEN @to_state::text = 'active' THEN @now::timestamptz ELSE i.activated_at END,
    updated_at       = @now::timestamptz
FROM (
    SELECT x.id, x.state
    FROM identities AS x
    WHERE x.id = ANY(@ids::text[])
      AND x.state = 'banned'
      AND x.ban_until IS NOT NULL
      AND x.ban_until <= @now::timestamptz
    FOR UPDATE SKIP LOCKED
) AS prev
WHERE i.id = prev.id
RETURNING i.id, i.hkey, i.site_id, i.client, i.type_id, prev.state AS from_state;

-- name: ActionReleaseDueQuarantines :many
UPDATE identities AS i
SET state            = 'pending',
    state_reason     = @reason::text,
    state_changed_at = @now::timestamptz,
    ban_until        = NULL,
    quarantine_until = NULL,
    updated_at       = @now::timestamptz
FROM (
    SELECT x.id, x.state
    FROM identities AS x
    WHERE x.state = 'quarantined'
      AND x.quarantine_until IS NOT NULL
      AND x.quarantine_until <= @now::timestamptz
    ORDER BY x.quarantine_until
    LIMIT @max_rows::int
    FOR UPDATE SKIP LOCKED
) AS prev
WHERE i.id = prev.id
RETURNING i.id, i.hkey, i.site_id, i.client, i.type_id, prev.state AS from_state;

-- name: ActionReleaseDueAccountBans :many
UPDATE accounts AS a
SET state      = 'active',
    ban_until  = NULL,
    updated_at = @now::timestamptz
FROM (
    SELECT x.id, x.state
    FROM accounts AS x
    WHERE x.state = 'banned'
      AND x.ban_until IS NOT NULL
      AND x.ban_until <= @now::timestamptz
    ORDER BY x.ban_until
    LIMIT @max_rows::int
    FOR UPDATE SKIP LOCKED
) AS prev
WHERE a.id = prev.id
RETURNING a.id, a.hkey, a.site_id, prev.state AS from_state;

-- name: ActionReleaseDueProxies :many
UPDATE proxies AS p
SET state            = 'active',
    state_reason     = @reason::text,
    state_changed_at = @now::timestamptz,
    ban_until        = NULL,
    updated_at       = @now::timestamptz
FROM (
    SELECT x.id, x.state
    FROM proxies AS x
    WHERE x.state IN ('banned', 'quarantined')
      AND x.ban_until IS NOT NULL
      AND x.ban_until <= @now::timestamptz
    ORDER BY x.ban_until
    LIMIT @max_rows::int
    FOR UPDATE SKIP LOCKED
) AS prev
WHERE p.id = prev.id
RETURNING p.id, p.hkey, p.namespace_id, prev.state AS from_state;

-- name: ActionSiteTenancy :many
SELECT s.id, s.namespace_id, n.tenant_id
FROM sites AS s
JOIN namespaces AS n ON n.id = s.namespace_id
WHERE s.id = ANY(@site_ids::text[]);

-- name: ActionNamespaceTenants :many
SELECT id, tenant_id FROM namespaces WHERE id = ANY(@namespace_ids::text[]);

-- name: ActionRecentExpiryReleases :many
-- Subjects released by the expiry job since a time; used to re-synchronize
-- the hot state after a leadership change (releases whose Redis push may have
-- failed on the previous leader).
SELECT DISTINCT e.subject_kind, e.subject_id, e.site_id, e.namespace_id
FROM state_events AS e
WHERE e.action IN ('unban', 'unquarantine')
  AND e.actor = 'system'
  AND e.reason IN ('ban_expired', 'quarantine_expired')
  AND e.shadow = false
  AND e.created_at >= @since::timestamptz
LIMIT @max_rows::int;
