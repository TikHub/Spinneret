-- Manual account operations (Operator).

-- name: ActionLoadAccount :one
SELECT a.id, a.hkey, a.site_id, a.state, a.ban_until, a.cooldown_until
FROM accounts AS a
JOIN sites AS s ON s.id = a.site_id
WHERE a.id = @id
  AND s.namespace_id = @namespace_id;

-- name: ActionTransitionAccount :one
UPDATE accounts AS a
SET state      = @to_state::text,
    ban_until  = sqlc.narg(ban_until)::timestamptz,
    updated_at = @changed_at::timestamptz
FROM (
    SELECT x.id, x.state
    FROM accounts AS x
    WHERE x.id = @id::text
      AND x.state = ANY(@from_states::text[])
    FOR UPDATE
) AS prev
WHERE a.id = prev.id
RETURNING a.id, a.hkey, prev.state AS from_state;

-- name: ActionCooldownAccount :one
UPDATE accounts
SET cooldown_until = GREATEST(COALESCE(cooldown_until, @until::timestamptz), @until::timestamptz),
    updated_at     = @changed_at::timestamptz
WHERE id = @id::text
RETURNING id, hkey, state, cooldown_until;

-- name: ActionCountAccountMembers :one
SELECT count(*)::int AS members FROM identities WHERE account_id = @account_id::text;
