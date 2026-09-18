-- name: NotifyAlertInsert :one
INSERT INTO alert_events (
    id, created_at, tenant_id, namespace_id, site_id, kind, severity, title, message, details, dedup_key
) VALUES (
    sqlc.arg(id), sqlc.arg(created_at), sqlc.arg(tenant_id), sqlc.arg(namespace_id), sqlc.arg(site_id),
    sqlc.arg(kind), sqlc.arg(severity), sqlc.arg(title), sqlc.arg(message), sqlc.arg(details), sqlc.arg(dedup_key)
)
RETURNING *;

-- NotifyAlertAppendDelivery atomically appends one delivery record (a JSON
-- object) to the deliveries array of an alert.
-- name: NotifyAlertAppendDelivery :execrows
UPDATE alert_events
SET deliveries = deliveries || jsonb_build_array(sqlc.arg(delivery)::jsonb)
WHERE id = sqlc.arg(id);

-- name: NotifyAlertGet :one
SELECT * FROM alert_events WHERE id = sqlc.arg(id);

-- NotifyAlertList pages alert events newest first (keyset on created_at, id).
-- Visibility: tenant-level alerts when include_tenant; alerts of the listed
-- namespaces; alerts of the listed sites.
-- name: NotifyAlertList :many
SELECT * FROM alert_events
WHERE tenant_id = sqlc.arg(tenant_id)
  AND ((namespace_id = '' AND sqlc.arg(include_tenant)::boolean)
       OR namespace_id = ANY(sqlc.arg(namespace_ids)::text[])
       OR (site_id <> '' AND site_id = ANY(sqlc.arg(site_ids)::text[])))
  AND (sqlc.arg(filter_namespace_id)::text = '' OR namespace_id = sqlc.arg(filter_namespace_id)::text)
  AND (sqlc.arg(filter_site_id)::text = '' OR site_id = sqlc.arg(filter_site_id)::text)
  AND (sqlc.arg(filter_kind)::text = '' OR kind = sqlc.arg(filter_kind)::text)
  AND (sqlc.arg(filter_severity)::text = '' OR severity = sqlc.arg(filter_severity)::text)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR created_at >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR created_at < sqlc.narg(to_at)::timestamptz)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.arg(cursor_id)::text))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit_rows);
