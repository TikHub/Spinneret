-- NotifyExpiredIdentityEvents pages the enforced transitions of identities to
-- "expired" in (created_at, id) order, strictly after the keyset position
-- (after_at, after_id). Served by the (action, created_at) index; all such
-- transitions are recorded with action "expire".
-- name: NotifyExpiredIdentityEvents :many
SELECT id, created_at, tenant_id, namespace_id, site_id, subject_id, from_state, reason
FROM state_events
WHERE action = 'expire'
  AND created_at >= sqlc.arg(after_at)::timestamptz
  AND (created_at, id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::text)
  AND subject_kind = 'identity'
  AND to_state = 'expired'
  AND from_state <> 'expired'
  AND NOT shadow
ORDER BY created_at, id
LIMIT sqlc.arg(limit_rows);

-- name: NotifyIdentityRefs :many
SELECT id, site_id, client, type_id FROM identities WHERE id = ANY(sqlc.arg(ids)::text[]);

-- name: NotifySettingGet :one
SELECT value FROM system_settings WHERE key = sqlc.arg(key);

-- name: NotifySettingPut :exec
INSERT INTO system_settings (key, value, updated_at)
VALUES (sqlc.arg(key), sqlc.arg(value), now())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
