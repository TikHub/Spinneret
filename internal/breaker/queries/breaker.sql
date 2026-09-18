-- name: BreakerInsertEvent :exec
-- Records one breaker transition or site switch (query names are prefixed
-- with "Breaker" to stay globally unique across private sqlc packages).
INSERT INTO breaker_events (
    id, created_at, tenant_id, namespace_id, site_id, endpoint_group_id,
    from_state, to_state, trigger, reason, open_until, metrics, actor
) VALUES (
    @id, @created_at, @tenant_id, @namespace_id, @site_id, @endpoint_group_id,
    @from_state, @to_state, @trigger, @reason, sqlc.narg(open_until), @metrics, @actor
);

-- name: BreakerListEvents :many
-- Keyset pagination over (created_at DESC, id DESC). Site restriction: when
-- all_sites is false only rows of site_ids are returned.
SELECT be.id,
       be.created_at,
       be.site_id,
       be.endpoint_group_id,
       be.from_state,
       be.to_state,
       be.trigger,
       be.reason,
       be.open_until,
       be.metrics,
       be.actor,
       COALESCE(s.name, '')::text    AS site_name,
       COALESCE(eg.name, '')::text   AS endpoint_group_name,
       COALESCE(eg.client, '')::text AS client
FROM breaker_events be
LEFT JOIN sites s ON s.id = be.site_id
LEFT JOIN endpoint_groups eg ON eg.id = be.endpoint_group_id
WHERE be.namespace_id = @namespace_id
  AND (@all_sites::boolean OR be.site_id = ANY (@site_ids::text[]))
  AND (sqlc.narg(site_id)::text IS NULL OR be.site_id = sqlc.narg(site_id)::text)
  AND (sqlc.narg(endpoint_group_id)::text IS NULL OR be.endpoint_group_id = sqlc.narg(endpoint_group_id)::text)
  AND (sqlc.narg(trigger_filter)::text IS NULL OR be.trigger = sqlc.narg(trigger_filter)::text)
  AND (sqlc.narg(start_at)::timestamptz IS NULL OR be.created_at >= sqlc.narg(start_at)::timestamptz)
  AND (sqlc.narg(end_at)::timestamptz IS NULL OR be.created_at < sqlc.narg(end_at)::timestamptz)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (be.created_at, be.id) < (sqlc.narg(cursor_at)::timestamptz, @cursor_id::text))
ORDER BY be.created_at DESC, be.id DESC
LIMIT @page_limit;

-- name: BreakerLockSite :one
SELECT paused, paused_reason, paused_at, paused_by
FROM sites
WHERE id = @id AND namespace_id = @namespace_id
FOR UPDATE;

-- name: BreakerUpdateSitePaused :one
UPDATE sites
SET paused        = @paused,
    paused_reason = @paused_reason,
    paused_at     = sqlc.narg(paused_at),
    paused_by     = @paused_by,
    updated_at    = now()
WHERE id = @id AND namespace_id = @namespace_id
RETURNING paused, paused_reason, paused_at, paused_by;

-- name: BreakerNamespace :one
SELECT id, tenant_id, name
FROM namespaces
WHERE id = @id;

-- name: BreakerNamespaceSites :many
SELECT id, hkey, name, paused, paused_reason, paused_at
FROM sites
WHERE namespace_id = @namespace_id
ORDER BY name;

-- name: BreakerGroupsByKeys :many
SELECT eg.hkey, eg.id, eg.site_id, eg.client, eg.name
FROM endpoint_groups eg
JOIN sites s ON s.id = eg.site_id
WHERE s.namespace_id = @namespace_id
  AND eg.hkey = ANY (@hkeys::bigint[]);
