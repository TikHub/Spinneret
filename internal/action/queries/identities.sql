-- Manual identity operations (Operator). Every transition locks the affected
-- rows, re-checks the allowed source states and returns the previous state so
-- that state events record the exact transition.

-- name: ActionLoadIdentities :many
SELECT i.id, i.hkey, i.site_id, i.client, i.type_id, i.account_id, i.state, i.state_changed_at
FROM identities AS i
JOIN sites AS s ON s.id = i.site_id
WHERE s.namespace_id = @namespace_id
  AND i.id = ANY(@ids::text[]);

-- name: ActionTransitionIdentities :many
UPDATE identities AS i
SET state            = @to_state::text,
    state_reason     = @reason::text,
    state_changed_at = @changed_at::timestamptz,
    ban_until        = sqlc.narg(ban_until)::timestamptz,
    quarantine_until = sqlc.narg(quarantine_until)::timestamptz,
    activated_at     = CASE WHEN @to_state::text = 'active' THEN @changed_at::timestamptz ELSE i.activated_at END,
    updated_at       = @changed_at::timestamptz
FROM (
    SELECT x.id, x.state
    FROM identities AS x
    WHERE x.id = ANY(@ids::text[])
      AND x.site_id = @site_id::text
      AND x.state = ANY(@from_states::text[])
    FOR UPDATE
) AS prev
WHERE i.id = prev.id
RETURNING i.id, i.hkey, i.client, i.type_id, prev.state AS from_state;

-- name: ActionBanAccountMembers :many
UPDATE identities AS i
SET state            = 'banned',
    state_reason     = @reason::text,
    state_changed_at = @changed_at::timestamptz,
    ban_until        = sqlc.narg(ban_until)::timestamptz,
    quarantine_until = NULL,
    updated_at       = @changed_at::timestamptz
FROM (
    SELECT x.id, x.state
    FROM identities AS x
    WHERE x.account_id = @account_id::text
      AND (
          x.state IN ('active', 'pending', 'quarantined', 'expired')
          -- Members disabled only because the account was disabled follow the
          -- account into the ban (and are released by its unban).
          OR (x.state = 'disabled' AND x.state_reason = 'account_disabled')
          OR (x.state = 'banned' AND x.ban_until IS NOT NULL
              AND (sqlc.narg(ban_until)::timestamptz IS NULL OR x.ban_until < sqlc.narg(ban_until)::timestamptz))
      )
    FOR UPDATE
) AS prev
WHERE i.id = prev.id
RETURNING i.id, i.hkey, i.client, i.type_id, prev.state AS from_state;

-- name: ActionSetAccountMembersState :many
UPDATE identities AS i
SET state            = @to_state::text,
    state_reason     = @reason::text,
    state_changed_at = @changed_at::timestamptz,
    ban_until        = NULL,
    quarantine_until = NULL,
    activated_at     = CASE WHEN @to_state::text = 'active' THEN @changed_at::timestamptz ELSE i.activated_at END,
    updated_at       = @changed_at::timestamptz
FROM (
    SELECT x.id, x.state
    FROM identities AS x
    WHERE x.account_id = @account_id::text
      AND x.state = ANY(@from_states::text[])
      AND (@only_reason::text = '' OR x.state_reason = @only_reason::text)
    FOR UPDATE
) AS prev
WHERE i.id = prev.id
RETURNING i.id, i.hkey, i.client, i.type_id, prev.state AS from_state;

-- name: ActionAccountIDsByKeys :many
SELECT id, hkey FROM accounts WHERE hkey = ANY(@hkeys::bigint[]);

-- name: ActionIdentityIDsByKeys :many
SELECT id, hkey FROM identities WHERE hkey = ANY(@hkeys::bigint[]);

-- name: ActionProxyIDsByKeys :many
SELECT id, hkey FROM proxies WHERE hkey = ANY(@hkeys::bigint[]);
